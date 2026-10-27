package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- ADR-012: exakte Origin-Prüfung ----------

func TestSameOriginExact(t *testing.T) {
	a := &App{cfg: defaultConfig()}
	a.cfg.PublicURL = "https://tb.firma.local"
	req := func(host string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("POST", "http://"+host+"/api/x", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, c := range []struct {
		host string
		hdr  map[string]string
		ok   bool
	}{
		{"intern:8080", nil, true}, // kein Browser-Kontext: CSRF-Token entscheidet
		{"intern:8080", map[string]string{"Origin": "https://tb.firma.local"}, true},
		{"intern:8080", map[string]string{"Origin": "https://TB.Firma.local:443"}, true},
		{"intern:8080", map[string]string{"Referer": "https://tb.firma.local/pfad?x=1"}, true},
		{"intern:8080", map[string]string{"Origin": "https://tb.firma.local.angreifer.example"}, false},
		{"intern:8080", map[string]string{"Origin": "https://tb.firma.localhost"}, false},
		{"intern:8080", map[string]string{"Origin": "https://tb.firma.local:8443"}, false},
		{"intern:8080", map[string]string{"Origin": "http://tb.firma.local"}, false},
		{"intern:8080", map[string]string{"Origin": "null"}, false},
		{"intern:8080", map[string]string{"Origin": "https://user@tb.firma.local"}, false},
		{"intern:8080", map[string]string{"Referer": "https://tb.firma.local.evil/"}, false},
		{"intern:8080", map[string]string{"Origin": "http://intern:8080"}, true}, // eigener Host
		{"intern:8080", map[string]string{"Origin": "http://intern:8080.evil"}, false},
		{"intern:8080", map[string]string{"Origin": "http://intern"}, false}, // anderer Port
	} {
		if got := a.sameOrigin(req(c.host, c.hdr)); got != c.ok {
			t.Errorf("Host %s, %v: %v, erwartet %v", c.host, c.hdr, got, c.ok)
		}
	}
}

// ---------- Konfiguration ----------

func writeSecret(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodSecret = "0123456789abcdefghijklmnopqrstuvwxyzABCDEF"

func commConfig(t *testing.T) CommunicationConfig {
	return CommunicationConfig{Enabled: true, PublicBaseURL: "https://kommunikation.firma.local/", InternalBaseURL: "http://ucng:8080",
		ServiceCredentialFile: writeSecret(t, goodSecret+"\n", 0o600), Features: defaultCommunicationFeatures}
}

func TestCommunicationConfig(t *testing.T) {
	var off CommunicationConfig
	if err := off.validate("https://tb.firma.local", false); err != nil {
		t.Fatalf("aus: keine Pflichtangaben, %v", err)
	}
	c := commConfig(t)
	if err := c.validate("https://tb.firma.local", false); err != nil {
		t.Fatal(err)
	}
	if c.PublicBaseURL != "https://kommunikation.firma.local" || c.Mode != "phonebook" || c.DefaultTranscriptRetentionDays != 14 || c.ProbeInterval.Duration != 30*time.Second || c.secret != goodSecret {
		t.Fatalf("Standardwerte: %+v", c)
	}
	bad := map[string]func(*CommunicationConfig){
		"http extern":         func(c *CommunicationConfig) { c.PublicBaseURL = "http://kommunikation.firma.local" },
		"Pfad (Präfix)":       func(c *CommunicationConfig) { c.PublicBaseURL = "https://tb.firma.local/communication/" },
		"gleiche Herkunft":    func(c *CommunicationConfig) { c.PublicBaseURL = "https://TB.firma.local:443" },
		"Zugangsdaten intern": func(c *CommunicationConfig) { c.InternalBaseURL = "http://admin:pw@ucng:8080" },
		"Abfrage":             func(c *CommunicationConfig) { c.InternalBaseURL = "http://ucng:8080/?x=1" },
		"relativ":             func(c *CommunicationConfig) { c.InternalBaseURL = "ucng:8080" },
		"Modus":               func(c *CommunicationConfig) { c.Mode = "standalone" },
		"ohne Geheimnis":      func(c *CommunicationConfig) { c.ServiceCredentialFile = "" },
		"Datei fehlt":         func(c *CommunicationConfig) { c.ServiceCredentialFile = "/gibt/es/nicht" },
		"zu kurz":             func(c *CommunicationConfig) { c.ServiceCredentialFile = writeSecret(t, "kurz", 0o600) },
		"für alle lesbar":     func(c *CommunicationConfig) { c.ServiceCredentialFile = writeSecret(t, goodSecret, 0o644) },
		"Transkripte 400":     func(c *CommunicationConfig) { c.DefaultTranscriptRetentionDays = 400 },
		"Transkripte -1":      func(c *CommunicationConfig) { c.DefaultTranscriptRetentionDays = -1 },
		"Intervall 1s":        func(c *CommunicationConfig) { c.ProbeInterval = Duration{time.Second} },
	}
	for name, mod := range bad {
		c := commConfig(t)
		mod(&c)
		if err := c.validate("https://tb.firma.local", false); err == nil {
			t.Errorf("%s: akzeptiert", name)
		}
	}
	local := commConfig(t)
	local.PublicBaseURL = "http://localhost:8095"
	if err := local.validate("", false); err != nil {
		t.Errorf("localhost über http (Entwicklung): %v", err)
	}
	// Funktionsschalter: fehlende Angaben → Standard (riskante Funktionen aus)
	var f struct {
		Features CommunicationFeatures `json:"features"`
	}
	if err := json.Unmarshal([]byte(`{"features":{"voice":false}}`), &f); err != nil || f.Features.Voice || !f.Features.Chat || f.Features.Federation || f.Features.Beacon {
		t.Fatalf("Funktionsschalter: %+v %v", f.Features, err)
	}
}

// ---------- Prüfung gegen ein simuliertes UCNG ----------

type fakeUCNG struct {
	settings string
	health   int
}

func (f *fakeUCNG) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			if f.health == http.StatusFound {
				http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
				return
			}
			w.WriteHeader(f.health)
			_, _ = w.Write([]byte("ok"))
		case "/api/settings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(f.settings))
		default:
			http.NotFound(w, r)
		}
	})
}

const secureSettings = `{"appMode":"hosted","hosted":{"guests":"none","registration":false,"federation":false},"voice":true,"about":{"version":"1.2.3"},"phonebook":{"authorize":"https://tb.firma.local/api/addons/communication/authorize","locks":["guests","registration","federation","channelFederation","beacon","externalGifs"],"transcriptKeepDays":14}}`

func commApp(t *testing.T, internal string) *App {
	t.Helper()
	a := testApp(t)
	a.cfg.Communication = commConfig(t)
	a.cfg.Communication.InternalBaseURL = internal
	if err := a.cfg.Communication.validate("", false); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCommunicationProbe(t *testing.T) {
	f := &fakeUCNG{settings: secureSettings, health: 200}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	a := commApp(t, srv.URL)
	if s := a.comm.snapshot(); s.Status != "pending" {
		t.Fatalf("vor der ersten Prüfung: %s", s.Status)
	}
	probe := func() commSnapshot {
		a.probeCommunication(context.Background(), commHTTPClient())
		return a.comm.snapshot()
	}
	if s := probe(); s.Status != "ok" || !s.Secure || s.Version != "1.2.3" || len(s.Problems) != 0 {
		t.Fatalf("sicheres Profil: %+v", s)
	}
	cases := map[string]struct {
		settings string
		want     []string
	}{
		"simple":        {`{"appMode":"simple","voice":true}`, []string{"Modus"}},
		"Gäste lesen":   {`{"appMode":"hosted","hosted":{"guests":"read","registration":true},"voice":true}`, []string{"LOCK_GUESTS", "LOCK_REGISTRATION"}},
		"Föderation":    {`{"appMode":"hosted","hosted":{"guests":"none","federation":true},"chanFed":{"domain":"x"},"voice":true}`, []string{"LOCK_FEDERATION", "LOCK_CHANNEL_FEDERATION"}},
		"Voice fehlt":   {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":false}`, []string{"VOICE"}},
		"kaputtes JSON": {`<html>`, []string{"nicht lesbar"}},
		"leere Antwort": {`{}`, []string{"Modus"}},
		// PH-02: UCNG muss im Modus phonebook laufen (keine zweite Anmeldung) und auf uns zeigen
		"ohne Modus phonebook": {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true}`, []string{"MODE=phonebook"}},
		// RC (ADR-009): ohne Sperren für Beacon und externe GIFs nicht einsatzbereit
		"ohne Beacon-/GIF-Sperre": {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true,"phonebook":{"authorize":"https://tb.firma.local/api/addons/communication/authorize","locks":["guests"]}}`, []string{"LOCK_BEACON", "LOCK_EXTERNAL_GIFS"}},
		// ADR-011: Transkripte nicht unbegrenzt bzw. länger als vereinbart; STT nur mit Freigabe
		"Transkripte unbegrenzt": {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true,"phonebook":{"authorize":"https://tb.firma.local/api/addons/communication/authorize","locks":["beacon","externalGifs"],"transcriptKeepDays":0}}`, []string{"unbegrenzt"}},
		"Transkripte zu lange":   {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true,"phonebook":{"authorize":"https://tb.firma.local/api/addons/communication/authorize","locks":["beacon","externalGifs"],"transcriptKeepDays":90}}`, []string{"90 Tage"}},
		"STT ohne Freigabe":      {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true,"phonebook":{"authorize":"https://tb.firma.local/api/addons/communication/authorize","locks":["beacon","externalGifs"],"transcriptKeepDays":14,"stt":true}}`, []string{"features.transcription"}},
		"fremdes Telefonbuch":    {`{"appMode":"hosted","hosted":{"guests":"none"},"voice":true,"phonebook":{"authorize":"https://tb.angreifer.example/api/addons/communication/authorize"}}`, []string{"PHONEBOOK_URL"}},
	}
	a.cfg.PublicURL = "https://tb.firma.local"
	for name, c := range cases {
		f.settings = c.settings
		s := probe()
		if s.Status == "ok" || s.Secure {
			t.Errorf("%s: als sicher gemeldet", name)
		}
		all := strings.Join(s.Problems, " | ")
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: Hinweis %q fehlt in %q", name, w, all)
			}
		}
	}
	f.settings = secureSettings
	f.health = http.StatusFound // Weiterleitung wird nicht verfolgt
	if s := probe(); s.Status != "unreachable" {
		t.Errorf("Weiterleitung: %s", s.Status)
	}
	f.health = 200
	srv.Close()
	if s := probe(); s.Status != "unreachable" || s.Reachable {
		t.Errorf("nicht erreichbar: %+v", s)
	}
}

// ---------- Routen, Sitzung, Readiness ----------

func sessionCookie(t *testing.T, a *App, user string, admin bool) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := a.createSession(context.Background(), rec, httptest.NewRequest("POST", "/api/login", nil), user, user, admin, "local", ""); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0]
}

func getWith(h http.Handler, path string, c *http.Cookie, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote + ":4000"
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestCommunicationDisabledIsInvisible(t *testing.T) {
	a := testApp(t) // Standard: aus
	h := a.routes(false)
	cookie := sessionCookie(t, a, "anna", true)
	if rec := getWith(h, "/api/addons/communication/status", cookie, "10.0.0.1"); rec.Code != http.StatusNotFound {
		t.Fatalf("aus: Status-Endpunkt antwortet %d", rec.Code)
	}
	rec := getWith(h, "/readyz", nil, "10.0.0.1")
	if strings.Contains(rec.Body.String(), "communication") {
		t.Fatalf("aus: Readiness erwähnt Kommunikation: %s", rec.Body)
	}
	if body := getWith(h, "/metrics", nil, "10.0.0.1").Body.String(); strings.Contains(body, "tb_communication") {
		t.Fatal("aus: Metriken zur Kommunikation")
	}
}

func TestCommunicationStatusEndpoint(t *testing.T) {
	f := &fakeUCNG{settings: secureSettings, health: 200}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	a := commApp(t, srv.URL)
	h := a.routes(false)
	// Gast (öffentliches Telefonbuch) bekommt nichts
	if rec := getWith(h, "/api/addons/communication/status", nil, "10.0.0.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("Gast: %d", rec.Code)
	}
	user, admin := sessionCookie(t, a, "anna", false), sessionCookie(t, a, "admin", true)
	var out map[string]any
	_ = json.Unmarshal(getWith(h, "/api/addons/communication/status", user, "10.0.0.1").Body.Bytes(), &out)
	if out["available"] != false || out["status"] != "pending" || out["message"] == nil || out["check"] != nil || out["internalBaseURL"] != nil {
		t.Fatalf("Person vor der Prüfung: %v", out)
	}
	if rec := getWith(h, "/readyz", nil, "10.0.0.1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"degraded"`) {
		t.Fatalf("Readiness vor der Prüfung: %d %s", rec.Code, rec.Body)
	}
	a.probeCommunication(context.Background(), commHTTPClient())
	out = nil
	_ = json.Unmarshal(getWith(h, "/api/addons/communication/status", admin, "10.0.0.1").Body.Bytes(), &out)
	if out["available"] != true || out["url"] != "https://kommunikation.firma.local" || out["check"] == nil || out["internalBaseURL"] != srv.URL {
		t.Fatalf("Admin: %v", out)
	}
	if rec := getWith(h, "/readyz", nil, "10.0.0.1"); !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("Readiness: %s", rec.Body)
	}
	if body := getWith(h, "/metrics", nil, "10.0.0.1").Body.String(); !strings.Contains(body, "tb_communication_secure 1") || !strings.Contains(body, "tb_communication_up 1") {
		t.Fatal("Metriken fehlen")
	}
	// UCNG unsicher → nicht verfügbar, Telefonbuch bleibt bereit (degraded, nie down)
	f.settings = `{"appMode":"simple","voice":true}`
	a.probeCommunication(context.Background(), commHTTPClient())
	out = nil
	_ = json.Unmarshal(getWith(h, "/api/addons/communication/status", user, "10.0.0.1").Body.Bytes(), &out)
	if out["available"] != false || out["status"] != "insecure" {
		t.Fatalf("unsicher: %v", out)
	}
	if rec := getWith(h, "/readyz", nil, "10.0.0.1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "LOCK_GUESTS") && !strings.Contains(rec.Body.String(), "Modus") {
		t.Fatalf("Readiness unsicher: %d %s", rec.Code, rec.Body)
	}
}

// Über den echten Konfigurationslader: fehlt „features“, gelten die Standardwerte.
func TestCommunicationConfigFileDefaults(t *testing.T) {
	dir := t.TempDir()
	secret := writeSecret(t, goodSecret, 0o600)
	p := filepath.Join(dir, "config.json")
	cfg := `{"auth":{"mode":"local"},"storage":{"mode":"json","jsonPath":"` + filepath.Join(dir, "tb.json") + `"},
	  "communication":{"enabled":true,"publicBaseURL":"https://kommunikation.firma.local","internalBaseURL":"http://ucng:8080","serviceCredentialFile":"` + secret + `"}}`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Communication.Features != defaultCommunicationFeatures {
		t.Fatalf("Standard-Funktionen: %+v", c.Communication.Features)
	}
	// Teilangabe: Rest bleibt Standard
	cfg2 := strings.Replace(cfg, `"serviceCredentialFile"`, `"features":{"voice":false},"serviceCredentialFile"`, 1)
	_ = os.WriteFile(p, []byte(cfg2), 0o600)
	if c, err = loadConfig(p); err != nil || c.Communication.Features.Voice || !c.Communication.Features.Chat {
		t.Fatalf("Teilangabe: %+v %v", c.Communication.Features, err)
	}
	// Standard ohne Block: aus, keine Pflichtangaben
	_ = os.WriteFile(p, []byte(`{"auth":{"mode":"local"},"storage":{"mode":"json","jsonPath":"`+filepath.Join(dir, "tb.json")+`"}}`), 0o600)
	if c, err = loadConfig(p); err != nil || c.Communication.Enabled {
		t.Fatalf("ohne Block: %+v %v", c.Communication, err)
	}
}

// Die Umgebungsvariablen aus compose.communication.yml schalten das Add-on ohne Konfigurationsblock ein.
func TestCommunicationConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{"auth":{"mode":"local"},"storage":{"mode":"json","jsonPath":"`+filepath.Join(dir, "tb.json")+`"}}`), 0o600)
	t.Setenv("TB_COMMUNICATION_ENABLED", "true")
	t.Setenv("TB_COMMUNICATION_PUBLIC_URL", "https://kommunikation.firma.local")
	t.Setenv("TB_COMMUNICATION_INTERNAL_URL", "http://ucng:8080")
	t.Setenv("TB_COMMUNICATION_SECRET_FILE", writeSecret(t, goodSecret, 0o440))
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Communication.Enabled || c.Communication.InternalBaseURL != "http://ucng:8080" || c.Communication.Features != defaultCommunicationFeatures {
		t.Fatalf("aus der Umgebung: %+v", c.Communication)
	}
}

// RC: Adresse, Proxys und Geheimnisse aus der Umgebung bzw. aus Dateien (Compose-Stack).
func TestConfigStackEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{"auth":{"mode":"local"},"storage":{"mode":"json","jsonPath":"`+filepath.Join(dir, "tb.json")+`"}}`), 0o600)
	tok := filepath.Join(dir, "metrics")
	_ = os.WriteFile(tok, []byte("metrik-token-1234\n"), 0o600)
	t.Setenv("TB_PUBLIC_URL", "https://telefonbuch.firma.local")
	t.Setenv("TB_TRUSTED_PROXIES", "172.30.10.0/24, 127.0.0.1")
	t.Setenv("TB_METRICS_TOKEN_FILE", tok)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://telefonbuch.firma.local" || strings.Join(c.TrustedProxies, "|") != "172.30.10.0/24|127.0.0.1" || c.Monitoring.Token != "metrik-token-1234" {
		t.Fatalf("Stack-Umgebung: %q %v %q", c.PublicURL, c.TrustedProxies, c.Monitoring.Token)
	}
	if c.Auth.ADRecheck.Duration != 15*time.Minute {
		t.Fatalf("auth.adRecheck Standard: %v", c.Auth.ADRecheck)
	}
}
