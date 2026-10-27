package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// PH-05: Direktnachricht aus der Visitenkarte, Zähler „neu seit letztem Besuch“.

func TestCommContact(t *testing.T) {
	e := ssoApp(t, nil)
	f := makeOrg(t, e.a)
	ctx := context.Background()
	ca, _ := e.login(t, "anna")
	cb, _ := e.login(t, "bert") // bert hat damit einen Principal, carl nicht (nie angemeldet)
	get := func(c *http.Cookie, path string) (int, map[string]any) {
		t.Helper()
		rec := getWith(e.h, path, c, "10.0.0.5")
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// Verzeichnis: „comm“ nur für Einträge mit Konto, nicht für sich selbst
	rec := getWith(e.h, "/api/directory", ca, "10.0.0.5")
	var dir struct{ Employees []Employee }
	json.Unmarshal(rec.Body.Bytes(), &dir)
	flags := map[int64]bool{}
	for _, x := range dir.Employees {
		flags[x.ID] = x.Comm
	}
	if !flags[f.bert] || !flags[f.carl] || flags[f.anna] {
		t.Fatalf("comm-Kennzeichen: %v (%s)", flags, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if rec = getWith(e.h, "/api/directory", nil, "10.0.0.5"); strings.Contains(rec.Body.String(), `"comm"`) {
		t.Fatal("Gast sieht comm-Kennzeichen")
	}

	pb, err := e.a.principalOfEmployee(ctx, &EmployeeRec{Username: "bert"})
	if err != nil {
		t.Fatal(err)
	}
	st, out := get(ca, "/api/addons/communication/contact/"+itoa64(f.bert))
	if st != 200 || out["url"] != e.a.cfg.Communication.PublicBaseURL+"/#pbdm:"+pb.ID {
		t.Fatalf("Kontakt Bert: %d %v", st, out)
	}
	for _, c := range []struct {
		id   int64
		want int
		msg  string
	}{
		{f.carl, 409, errCommNotYet.Error()},
		{f.dora, 404, ""}, // nicht aktiv
		{f.anna, 400, ""}, // sich selbst
		{999999, 404, ""},
	} {
		if st, out := get(ca, "/api/addons/communication/contact/"+itoa64(c.id)); st != c.want || (c.msg != "" && out["error"] != c.msg) {
			t.Fatalf("Kontakt %d: %d %v", c.id, st, out)
		}
	}
	if st, _ := get(nil, "/api/addons/communication/contact/"+itoa64(f.bert)); st != 401 {
		t.Fatalf("Gast: %d", st)
	}
	// Bert im AD gesperrt: nicht erreichbar
	pb.Disabled = true
	e.a.st().UpsertPrincipal(ctx, pb)
	if st, out := get(ca, "/api/addons/communication/contact/"+itoa64(f.bert)); st != 409 || out["error"] != errCommNoAccess.Error() {
		t.Fatalf("gesperrt: %d %v", st, out)
	}
	pb.Disabled = false
	e.a.st().UpsertPrincipal(ctx, pb)
	// Bert ohne Nutzungsrecht: nicht erreichbar; Bert selbst darf niemanden anschreiben
	pol := CommPolicy{Access: CommAccess{Users: []string{"anna"}}}
	pol.Roles = e.a.cfg.Communication.Roles
	b, _ := json.Marshal(pol)
	e.a.st().SetSetting(ctx, settingCommPolicy, string(b))
	e.a.commCfgInvalidate()
	if st, out := get(ca, "/api/addons/communication/contact/"+itoa64(f.bert)); st != 409 || out["error"] != errCommNoAccess.Error() {
		t.Fatalf("ohne Nutzungsrecht: %d %v", st, out)
	}
	if st, _ := get(cb, "/api/addons/communication/contact/"+itoa64(f.anna)); st != 403 {
		t.Fatalf("Anfrage ohne eigenes Nutzungsrecht: %d", st)
	}
}

func TestCommNews(t *testing.T) {
	e := ssoApp(t, nil)
	ca, _ := e.login(t, "anna")
	news := func() map[string]any {
		t.Helper()
		rec := getWith(e.h, "/api/addons/communication/news", ca, "10.0.0.5")
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("news: %d %s", rec.Code, rec.Header())
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := news(); out["available"] != true || out["dms"] != float64(2) || out["mentions"] != float64(1) || out["since"] != nil {
		t.Fatalf("Zähler: %v", out)
	}
	news() // aus dem Zwischenspeicher
	e.sink.mu.Lock()
	n := e.sink.news
	e.sink.mu.Unlock()
	if n != 1 {
		t.Fatalf("Anfragen an UCNG: %d (erwartet 1)", n)
	}
	if rec := getWith(e.h, "/api/addons/communication/news", nil, "10.0.0.5"); rec.Code != 401 {
		t.Fatalf("Gast: %d", rec.Code)
	}
	// UCNG nicht geprüft/erreichbar: kein Zähler, kein Fehler
	e.a.comm.mu.Lock()
	e.a.comm.reachable = false
	e.a.comm.mu.Unlock()
	if out := news(); out["available"] != false {
		t.Fatalf("UCNG nicht erreichbar: %v", out)
	}
	// falscher Schlüssel beim Empfänger: kein Zähler
	e.a.comm.mu.Lock()
	e.a.comm.reachable = true
	e.a.comm.mu.Unlock()
	e.a.commNews.mu.Lock()
	e.a.commNews.m = nil
	e.a.commNews.mu.Unlock()
	e.sink.mu.Lock()
	e.sink.key = "falsch"
	e.sink.mu.Unlock()
	if out := news(); out["available"] != false {
		t.Fatalf("Fehler bei UCNG: %v", out)
	}
}
