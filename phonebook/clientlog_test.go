package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientEventInput(t *testing.T) {
	now := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	in := clientEventInput{Event: "START", Hostname: "pc-it-042", Username: `STADT\mmuster`, IPs: flexStrings{"10.1.2.3", "fe80::1%12", "127.0.0.1", "10.1.2.3", "kaputt"},
		ClientTime: "2026-10-26T09:00:00+01:00", Version: "1.4", Reason: "unbekannt"}
	e, err := in.toEvent(now, "10.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if e.Event != "start" || e.Hostname != "PC-IT-042" || e.ClientIPs != "10.1.2.3, fe80::1" || e.ClientAt == nil || !e.ClientAt.Equal(now) || e.Reason != "" || e.RemoteIP != "10.9.9.9" {
		t.Fatalf("Ereignis: %+v", e)
	}
	for _, bad := range []clientEventInput{{Event: "reboot", Hostname: "pc"}, {Event: "stop", Hostname: "pc<script>"}, {Event: "stop", Hostname: "pc", Username: "a\"b"}} {
		if _, err := bad.toEvent(now, ""); err == nil {
			t.Errorf("akzeptiert: %+v", bad)
		}
	}
	var f flexStrings
	if err := f.UnmarshalJSON([]byte(`"10.0.0.1, 10.0.0.2"`)); err != nil || len(f) != 2 {
		t.Fatalf("flexStrings: %v %v", f, err)
	}
	if csvSafe("=cmd|' /C calc'!A0") != "'=cmd|' /C calc'!A0" || csvSafe("PC1") != "PC1" {
		t.Fatal("csvSafe")
	}
}

func exerciseClientEvents(t *testing.T, st Store) {
	ctx := context.Background()
	if _, err := st.PruneClientEvents(ctx, time.Now().Add(time.Hour)); err != nil { // Altbestand aus früheren Läufen
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Truncate(time.Millisecond)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, e := range []ClientEvent{
		{At: old, Event: "start", Hostname: "PC-ALT", Username: `STADT\alt`, RemoteIP: "10.0.0.9"},
		{At: now, Event: "start", Hostname: "PC-1", Username: `STADT\anna`, ClientIPs: "10.0.0.1", RemoteIP: "10.0.0.1", Version: "1.4", ClientAt: &now},
		{At: now.Add(time.Second), Event: "start", Hostname: "PC-2", Username: `STADT\bernd`, RemoteIP: "10.0.0.2"},
		{At: now.Add(2 * time.Second), Event: "stop", Reason: "beenden", Hostname: "PC-1", Username: `STADT\anna`, RemoteIP: "10.0.0.1"},
	} {
		if err := st.AddClientEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := st.ListClientEvents(ctx, ClientEventFilter{})
	if err != nil || len(all) != 4 || all[0].Event != "stop" || all[0].Reason != "beenden" || all[3].Hostname != "PC-ALT" {
		t.Fatalf("Liste: %+v %v", all, err)
	}
	if l, _ := st.ListClientEvents(ctx, ClientEventFilter{Query: "anna"}); len(l) != 2 {
		t.Fatalf("Suche: %+v", l)
	}
	if l, _ := st.ListClientEvents(ctx, ClientEventFilter{Query: "10.0.0.2"}); len(l) != 1 || l[0].Hostname != "PC-2" {
		t.Fatalf("Suche IP: %+v", l)
	}
	if l, _ := st.ListClientEvents(ctx, ClientEventFilter{Host: "PC-1", Limit: 1}); len(l) != 1 || l[0].Event != "stop" {
		t.Fatalf("Host+Limit: %+v", l)
	}
	if l, _ := st.ListClientEvents(ctx, ClientEventFilter{Query: "50%_"}); len(l) != 0 {
		t.Fatalf("LIKE-Platzhalter nicht maskiert: %+v", l)
	}
	last, err := st.LatestClientEvents(ctx)
	if err != nil || len(last) != 3 || last[0].Hostname != "PC-1" || last[0].Event != "stop" || last[1].Hostname != "PC-2" {
		t.Fatalf("Letzte je Rechner: %+v %v", last, err)
	}
	withAt := all[2] // PC-1 start mit Client-Zeit
	if withAt.ClientAt == nil || !withAt.ClientAt.Equal(now) || withAt.Version != "1.4" {
		t.Fatalf("Felder: %+v", withAt)
	}
	n, err := st.PruneClientEvents(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("Bereinigen: %d %v", n, err)
	}
	if l, _ := st.ListClientEvents(ctx, ClientEventFilter{}); len(l) != 3 {
		t.Fatalf("nach Bereinigen: %d", len(l))
	}
}

func TestClientEventsJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tb.json")
	st, err := newJSONStore(p, "")
	if err != nil {
		t.Fatal(err)
	}
	exerciseClientEvents(t, st)
	// neu laden: Datei wird gelesen, kaputte Zeilen übersprungen, IDs laufen weiter
	f, _ := os.OpenFile(filepath.Join(dir, "tb.clients.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{kaputt\n")
	f.Close()
	st2, err := newJSONStore(p, "")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := st2.ListClientEvents(context.Background(), ClientEventFilter{})
	if len(l) != 3 {
		t.Fatalf("nach Neuladen: %d", len(l))
	}
	_ = st2.AddClientEvent(context.Background(), ClientEvent{At: time.Now(), Event: "start", Hostname: "PC-3"})
	l, _ = st2.ListClientEvents(context.Background(), ClientEventFilter{})
	if l[0].ID <= l[1].ID {
		t.Fatalf("IDs: %d %d", l[0].ID, l[1].ID)
	}
}

func testApp(t *testing.T) *App {
	t.Helper()
	st, err := newJSONStore(filepath.Join(t.TempDir(), "tb.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	c.Storage.Mode = "json"
	c.Auth.Mode = "local"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: c, m: newMetrics(), clientRL: newRateLimit(3, time.Minute), limiter: newLimiter(5, 5, time.Minute)}
	a.setStore(st)
	return a
}

func doReq(h http.Handler, method, path, body, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote + ":5000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClientEventHTTP(t *testing.T) {
	a := testApp(t)
	h := a.routes(false)
	ev := `{"event":"start","hostname":"PC-1","username":"STADT\\anna","ips":"10.0.0.1","clientTime":"2026-10-26T08:00:00+01:00","version":"1.4","extra":"egal"}`
	if rec := doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("abgeschaltet: %d", rec.Code)
	}
	a.cfg.ClientLog.Enabled = true
	a.cfg.ClientLog.Key = "geheim"
	if rec := doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Schlüssel: %d", rec.Code)
	}
	key := map[string]string{"X-TB-Client-Key": "geheim"}
	if rec := doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", key); rec.Code != http.StatusNoContent {
		t.Fatalf("Meldung: %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", map[string]string{"X-TB-Client-Key": "geheim", "Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("fremder Origin: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/api/client/event", strings.NewReader(ev))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-TB-Client-Key", "geheim")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", rec.Code)
	}
	// Ratenbegrenzung (3/min im Test): 1 ok + 1 Origin-Ablehnung zählt nicht … weitere bis zur Grenze
	_ = doReq(h, "POST", "/api/client/event", strings.Replace(ev, "start", "stop", 1), "10.0.0.1", key)
	_ = doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", key)
	if rec := doReq(h, "POST", "/api/client/event", ev, "10.0.0.1", key); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Ratenbegrenzung: %d", rec.Code)
	}
	l, _ := a.st().ListClientEvents(context.Background(), ClientEventFilter{})
	if len(l) != 3 || l[2].Username != `STADT\anna` || l[2].RemoteIP != "10.0.0.1" || l[2].ClientAt == nil {
		t.Fatalf("gespeichert: %+v", l)
	}
	// Admin-Liste ohne Anmeldung: verboten
	if rec := doReq(h, "GET", "/api/admin/clients", "", "10.0.0.1", nil); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("Admin-Liste ohne Anmeldung: %d", rec.Code)
	}
}

func TestMonitoringEndpoints(t *testing.T) {
	a := testApp(t)
	h := a.routes(false)
	if rec := doReq(h, "GET", "/livez", "", "203.0.113.5", nil); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("livez: %d", rec.Code)
	}
	// readyz: öffentlich nur der Status, intern mit Details
	rec := doReq(h, "GET", "/readyz", "", "203.0.113.5", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "checks") || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("readyz öffentlich: %d %s", rec.Code, rec.Body)
	}
	rec = doReq(h, "GET", "/readyz", "", "10.1.1.1", nil)
	if !strings.Contains(rec.Body.String(), `"storage"`) || !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("readyz intern: %s", rec.Body)
	}
	a.offline.Store(true)
	if rec := doReq(h, "GET", "/readyz", "", "10.1.1.1", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "degraded") {
		t.Fatalf("readyz Rückfall: %d %s", rec.Code, rec.Body)
	}
	a.offline.Store(false)
	// metrics: aus dem Internet verboten, intern erlaubt
	if rec := doReq(h, "GET", "/metrics", "", "203.0.113.5", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("metrics extern: %d", rec.Code)
	}
	_ = doReq(h, "GET", "/api/directory", "", "10.1.1.1", nil)
	_ = doReq(h, "POST", "/api/login", `{"username":"x","password":"y"}`, "10.1.1.1", nil)
	rec = doReq(h, "GET", "/metrics", "", "10.1.1.1", nil)
	body := rec.Body.String()
	for _, want := range []string{
		`tb_http_requests_total{route="/api/directory",method="GET",code="2xx"} 1`,
		`tb_logins_total{result="failed"} 1`,
		`tb_storage_up 1`, `tb_storage_info{mode="json"} 1`, `tb_employees{state="active"} 0`,
		`tb_http_request_duration_seconds_bucket{le="+Inf"}`, "go_goroutines", "# TYPE tb_http_requests_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics: %q fehlt", want)
		}
	}
	// Token gesetzt: auch intern nur mit Token
	a.cfg.Monitoring.Token = "t0ken"
	if rec := doReq(h, "GET", "/metrics", "", "10.1.1.1", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Token: %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/metrics", "", "203.0.113.5", map[string]string{"Authorization": "Bearer t0ken"}); rec.Code != 200 {
		t.Fatalf("mit Token: %d", rec.Code)
	}
	off := false
	a.cfg.Monitoring.Metrics = &off
	if rec := doReq(h, "GET", "/metrics", "", "10.1.1.1", map[string]string{"Authorization": "Bearer t0ken"}); rec.Code != http.StatusNotFound {
		t.Fatalf("abgeschaltet: %d", rec.Code)
	}
}
