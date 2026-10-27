// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePhonebook bildet die Server-zu-Server-Schnittstelle des Telefonbuchs nach.
type fakePhonebook struct {
	t      *testing.T
	ts     *httptest.Server
	cfg    PhonebookConfig
	mu     sync.Mutex
	codes  map[string]fakeCode
	sess   map[string]pbResult // Ergebnis der Nachprüfung je Sitzung
	down   bool
	calls  int
	active map[string]bool
	status map[string]string // Austritt: Status je Subjekt (Standard active)
	avatar map[string][]byte // PH-06: Startbild je Subjekt
	avHits int
}

type fakeCode struct {
	challenge string
	claims    pbClaims
}

func s256(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (f *fakePhonebook) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.cfg.key(pbKeyUCNGToPhonebook) {
		http.Error(w, "auth", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/internal/communication/redeem":
		var in struct{ Code, Verifier string }
		json.NewDecoder(r.Body).Decode(&in)
		c, ok := f.codes[in.Code]
		delete(f.codes, in.Code) // einmalig, auch bei falschem verifier
		if !ok || s256(in.Verifier) != c.challenge {
			http.Error(w, "code", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(c.claims)
	case "/api/internal/communication/validate":
		var in struct{ Sessions []pbCheck }
		json.NewDecoder(r.Body).Decode(&in)
		var out []pbResult
		for _, s := range in.Sessions {
			if s.Active {
				f.active[s.Session] = true
			}
			if res, ok := f.sess[s.Session]; ok {
				res.Session = s.Session
				out = append(out, res)
			} else {
				out = append(out, pbResult{Session: s.Session, Valid: false, Reason: "logout"})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	case "/api/internal/communication/principals":
		var in struct{ Subjects []string }
		json.NewDecoder(r.Body).Decode(&in)
		var out []pbStatus
		for _, sub := range in.Subjects {
			st := f.status[sub]
			if st == "" {
				st = "active"
			}
			out = append(out, pbStatus{Subject: sub, Status: st})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	default:
		if sub, ok := strings.CutPrefix(r.URL.Path, "/api/internal/communication/avatar/"); ok && r.Method == http.MethodGet && f.avatar[sub] != nil {
			f.avHits++
			w.Header().Set("Content-Type", "image/png")
			w.Write(f.avatar[sub])
			return
		}
		http.NotFound(w, r)
	}
}

// issue legt einen Code an (wie authorize im Telefonbuch) und liefert Code und verifier.
func (f *fakePhonebook) issue(c pbClaims) (code, verifier string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	code = base64.RawURLEncoding.EncodeToString(rnd(24))
	verifier = base64.RawURLEncoding.EncodeToString(rnd(32))
	if c.SessionExpires.IsZero() {
		c.SessionExpires = time.Now().Add(8 * time.Hour)
	}
	f.codes[code] = fakeCode{challenge: s256(verifier), claims: c}
	cc := c
	f.sess[c.Session] = pbResult{Valid: true, Claims: &cc}
	return code, verifier
}

func (f *fakePhonebook) set(session string, r pbResult) {
	f.mu.Lock()
	f.sess[session] = r
	f.mu.Unlock()
}

func (f *fakePhonebook) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func writeSecret(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, perm)
	return p
}

type pbEnv struct {
	*hostedEnv
	pb *fakePhonebook
}

func phonebookServer(t *testing.T) *pbEnv { t.Helper(); return phonebookServerWith(t, nil) }

func phonebookServerWith(t *testing.T, mod func(*Config)) *pbEnv {
	t.Helper()
	f := &fakePhonebook{t: t, codes: map[string]fakeCode{}, sess: map[string]pbResult{}, active: map[string]bool{}, status: map[string]string{}, avatar: map[string][]byte{}}
	f.ts = httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(f.ts.Close)
	pc := PhonebookConfig{Enabled: true, URL: f.ts.URL, SecretFile: writeSecret(t, strings.Repeat("s3cr3t-", 8), 0o600), RetentionDays: 90}
	if err := pc.load(); err != nil {
		t.Fatal(err)
	}
	f.cfg = pc
	cfg := Config{MaxRooms: 5, RateLimit: 100, RateWindow: time.Second, ClaimLimit: 100, AdminPassword: testAdminPW, BotRate: 1000,
		Mode: ModeHosted, PublicURL: "https://chat.example.org", Locks: OpLocks{}.forPhonebook(), Phonebook: pc}
	if mod != nil {
		mod(&cfg)
	}
	hub := NewHub(cfg.MaxRooms)
	users := NewUsers(time.Hour, 100)
	clock := &testClock{t: time.Now()}
	users.now = clock.now
	srv := NewServer(cfg, hub, users)
	srv.acctRL = newRateLimiter(1000, time.Minute)
	srv.unlockRL = newRateLimiter(1000, time.Minute)
	hub.EnsureDefaultRoom()
	ctx, cancel := context.WithCancel(context.Background())
	go srv.webhooks.run(ctx)
	t.Cleanup(cancel)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		close(srv.done)
		if srv.voice != nil {
			srv.voice.Close()
		}
		ts.Close()
	})
	return &pbEnv{&hostedEnv{ts, hub, srv, users, clock}, f}
}

func claimsFor(subject, user, display, role string) pbClaims {
	return pbClaims{Subject: subject, Username: user, DisplayName: display, Role: role, Session: "sess-" + base64.RawURLEncoding.EncodeToString(rnd(9)),
		SessionExpires: time.Now().Add(8 * time.Hour)}
}

// exchange meldet über den nachgebildeten Ablauf an und liefert die Antwort.
func (e *pbEnv) exchange(t *testing.T, c pbClaims) (*http.Response, map[string]any) {
	t.Helper()
	code, ver := e.pb.issue(c)
	res := do(t, "POST", e.ts.URL+"/api/integration/phonebook/exchange", "", jsonBody(map[string]string{"code": code, "verifier": ver}))
	return res, jmap(t, res)
}

func (e *pbEnv) signIn(t *testing.T, c pbClaims) (token, id string) {
	t.Helper()
	res, out := e.exchange(t, c)
	if res.StatusCode != 200 {
		t.Fatalf("Anmeldung %s: %d %v", c.Username, res.StatusCode, out)
	}
	s := out["session"].(map[string]any)
	return s["token"].(string), s["id"].(string)
}

func status(t *testing.T, method, url, token, body string) int {
	t.Helper()
	res := do(t, method, url, token, body)
	res.Body.Close()
	return res.StatusCode
}

func TestPhonebookConfig(t *testing.T) {
	good := writeSecret(t, strings.Repeat("x", 40), 0o640)
	for _, c := range []struct {
		name string
		cfg  PhonebookConfig
		ok   bool
	}{
		{"gültig", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", SecretFile: good}, true},
		{"localhost http", PhonebookConfig{Enabled: true, URL: "http://127.0.0.1:8080", SecretFile: good}, true},
		{"http extern", PhonebookConfig{Enabled: true, URL: "http://tb.firma.local", SecretFile: good}, false},
		// ADR-027: intern http nur mit Dienstnamen im Docker-Netz (ohne Punkt, keine IP)
		{"intern Dienstname http", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", InternalURL: "http://app:8080", SecretFile: good}, true},
		{"intern http mit Punkt", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", InternalURL: "http://tb.firma.local:8080", SecretFile: good}, false},
		{"intern http IP", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", InternalURL: "http://172.30.10.5:8080", SecretFile: good}, false},
		{"intern http IPv6", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", InternalURL: "http://[fd00::5]:8080", SecretFile: good}, false},
		{"öffentlich Dienstname http", PhonebookConfig{Enabled: true, URL: "http://app:8080", SecretFile: good}, false},
		{"mit Pfad", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local/tb", SecretFile: good}, false},
		{"mit Zugangsdaten", PhonebookConfig{Enabled: true, URL: "https://a:b@tb.firma.local", SecretFile: good}, false},
		{"ohne Geheimnis", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local"}, false},
		{"Geheimnis für alle lesbar", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", SecretFile: writeSecret(t, strings.Repeat("x", 40), 0o644)}, false},
		{"Geheimnis zu kurz", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", SecretFile: writeSecret(t, "kurz", 0o600)}, false},
		{"Frist kürzer als Prüfabstand", PhonebookConfig{Enabled: true, URL: "https://tb.firma.local", SecretFile: good, Revalidate: 10 * time.Minute, Grace: 5 * time.Minute}, false},
	} {
		err := c.cfg.load()
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// Schlüssel je Richtung verschieden
	pc := PhonebookConfig{secret: []byte(strings.Repeat("x", 40))}
	if pc.key(pbKeyUCNGToPhonebook) == pc.key(pbKeyPhonebookToUCNG) {
		t.Fatal("gleicher Schlüssel für beide Richtungen")
	}
	// Sperren: im Modus phonebook immer alle, Gastzugang lässt sich nicht öffnen
	l := OpLocks{}.forPhonebook()
	if err := l.validate("phonebook"); err != nil || l.Guests != GuestsNone || !l.NoRegistration || !l.NoFederation || !l.NoChannelFederation {
		t.Fatalf("Sperren: %+v %v", l, err)
	}
	if err := (OpLocks{Guests: GuestsRead}).forPhonebook().validate("phonebook"); err == nil {
		t.Fatal("LOCK_GUESTS=read im Modus phonebook angenommen")
	}
}

// Passwort-Anmeldung, Registrierung, Einladungen, E-Mail und 2FA gibt es im Modus phonebook nicht;
// Gäste sehen nichts.
func TestPhonebookClosedEndpoints(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	st := jmap(t, do(t, "GET", base+"/api/settings", "", ""))
	pbs, _ := st["phonebook"].(map[string]any)
	if pbs == nil || pbs["authorize"] != e.pb.ts.URL+"/api/addons/communication/authorize" {
		t.Fatalf("settings.phonebook: %v", st["phonebook"])
	}
	if h := st["hosted"].(map[string]any); h["guests"] != "none" || h["registration"] != false || h["federation"] != false {
		t.Fatalf("settings.hosted: %v", h)
	}
	for _, p := range []string{"prelogin", "register", "login", "resend", "forgot", "reset", "password", "email", "2fa/begin", "recovery", "keys", "delete"} {
		res := do(t, "POST", base+"/api/account/"+p, "", jsonBody(map[string]any{"login": "x", "name": "x"}))
		out := jmap(t, res)
		if res.StatusCode != 404 || out["phonebook"] != true {
			t.Errorf("POST /api/account/%s: %d %v", p, res.StatusCode, out)
		}
	}
	for _, p := range []string{"/api/account/invite?t=x", "/api/account/reset?t=x", "/api/account/verify?t=x"} {
		if c := status(t, "GET", base+p, "", ""); c != 404 {
			t.Errorf("GET %s: %d", p, c)
		}
	}
	adm := jmap(t, do(t, "POST", base+"/api/admin/login", "", jsonBody(map[string]string{"password": testAdminPW})))["token"].(string)
	if c := status(t, "POST", base+"/api/admin/invites", adm, `{"note":"x"}`); c != 404 {
		t.Errorf("Admin-Einladung: %d", c)
	}
	if c := status(t, "PUT", base+"/api/admin/hosted", adm, `{"guests":"write"}`); c != 409 {
		t.Errorf("Gastzugang öffnen: %d", c)
	}
	for _, p := range []string{"/api/rooms", "/api/rooms/lobby/messages", "/api/profiles"} {
		if c := status(t, "GET", base+p, "", ""); c != 401 {
			t.Errorf("Gast %s: %d", p, c)
		}
	}
	if c := status(t, "POST", base+"/api/session", "", `{"name":"Gast"}`); c != 403 {
		t.Errorf("Gastname: %d", c)
	}
	// Die Server-zu-Server-Endpunkte gibt es im Modus hosted nicht
	h := hostedServer(t)
	for _, p := range []string{"/api/integration/phonebook/exchange", "/api/integration/phonebook/revoke", "/api/account/e2ee/setup"} {
		if c := status(t, "POST", h.ts.URL+p, "", "{}"); c != 404 {
			t.Errorf("hosted %s: %d", p, c)
		}
	}
}

func TestPhonebookExchange(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	c := claimsFor("p_anna", "aschmidt", "Anna Schmidt", "")
	code, ver := e.pb.issue(c)
	ex := func(code, ver string) (int, map[string]any) {
		res := do(t, "POST", base+"/api/integration/phonebook/exchange", "", jsonBody(map[string]string{"code": code, "verifier": ver}))
		return res.StatusCode, jmap(t, res)
	}
	// falscher verifier (Code abgefangen, PKCE fehlt) – danach ist der Code verbraucht
	if st, out := ex(code, base64.RawURLEncoding.EncodeToString(rnd(32))); st != 401 || out["retry"] != true {
		t.Fatalf("falscher verifier: %d %v", st, out)
	}
	if st, _ := ex(code, ver); st != 401 {
		t.Fatalf("Code nach Fehlversuch noch gültig: %d", st)
	}
	code, ver = e.pb.issue(c)
	st, out := ex(code, ver)
	if st != 200 || out["created"] != true {
		t.Fatalf("Anmeldung: %d %v", st, out)
	}
	tok := out["session"].(map[string]any)["token"].(string)
	id := out["session"].(map[string]any)["id"].(string)
	// wiederverwendeter Code
	if st, _ := ex(code, ver); st != 401 {
		t.Fatalf("Code zweimal eingelöst: %d", st)
	}
	// unbrauchbare Eingaben erreichen das Telefonbuch gar nicht
	before := e.pb.calls
	for _, in := range [][2]string{{"", ver}, {"a b", ver}, {code, "kurz"}, {strings.Repeat("a", 200), ver}} {
		if st, _ := ex(in[0], in[1]); st != 401 {
			t.Errorf("Eingabe %q: %d", in, st)
		}
	}
	if e.pb.calls != before {
		t.Fatalf("ungültige Eingaben an das Telefonbuch weitergereicht")
	}
	me := jmap(t, do(t, "GET", base+"/api/session", tok, ""))
	if me["name"] != "Anna Schmidt" || me["member"] != true {
		t.Fatalf("Sitzung: %v", me)
	}
	acc := jmap(t, do(t, "GET", base+"/api/account", tok, ""))
	if ext, _ := acc["external"].(map[string]any); ext == nil || ext["username"] != "aschmidt" || ext["provider"] != "phonebook" {
		t.Fatalf("Konto: %v", acc)
	}
	// gleiche Person, zweites Gerät → gleiches Konto
	_, id2 := e.signIn(t, c)
	if id2 != id {
		t.Fatalf("zweite Anmeldung legt neues Konto an: %s / %s", id, id2)
	}
	// andere Person mit gleichem Anzeigenamen → eigenes Konto, Name eindeutig
	tok3, id3 := e.signIn(t, claimsFor("p_anna2", "aschmidt2", "Anna Schmidt", ""))
	if id3 == id {
		t.Fatal("anderes Subjekt teilt Konto")
	}
	if n := jmap(t, do(t, "GET", base+"/api/session", tok3, ""))["name"]; n != "Anna Schmidt (aschmidt2)" {
		t.Fatalf("Name bei Gleichstand: %v", n)
	}
	// Ein vorhandenes Konto gleichen Namens (z. B. aus der Zeit vor dem Modus phonebook) wird
	// nie über den Namen übernommen.
	if _, _, _, err := e.users.Register(RegisterInput{Name: "bweber", Keys: testKeys()}, false); err != nil {
		t.Fatal(err)
	}
	tok4, _ := e.signIn(t, claimsFor("p_bernd", "bweber", "", ""))
	if n := jmap(t, do(t, "GET", base+"/api/session", tok4, ""))["name"]; n == "bweber" {
		t.Fatal("lokales Konto über den Namen übernommen")
	}
	// Anzeigename geändert → beim nächsten Anmelden übernommen
	c2 := c
	c2.DisplayName, c2.Session = "Anna Schmidt-Berg", "sess-neu"
	tok5, _ := e.signIn(t, c2)
	if n := jmap(t, do(t, "GET", base+"/api/session", tok5, ""))["name"]; n != "Anna Schmidt-Berg" {
		t.Fatalf("Umbenennung: %v", n)
	}
	// geschützte Namen (Standard: „admin“) werden nicht vergeben, sondern ein lesbarer Ersatz
	tok6, _ := e.signIn(t, claimsFor("p_root01", "admin", "", ""))
	if n, _ := jmap(t, do(t, "GET", base+"/api/session", tok6, ""))["name"].(string); n != "admin" && n != "Konto root01" {
		t.Fatalf("Name für geschützten Benutzernamen: %q", n)
	}
	// unvollständige Angaben aus dem Telefonbuch
	bad := claimsFor("p_x", "x", "X", "superuser")
	if res, out := e.exchange(t, bad); res.StatusCode != 401 {
		t.Fatalf("ungültige Rolle: %d %v", res.StatusCode, out)
	}
	// Telefonbuch nicht erreichbar
	code, ver = e.pb.issue(claimsFor("p_y", "y", "Y", ""))
	e.pb.setDown(true)
	if st, _ := ex(code, ver); st != 503 {
		t.Fatalf("Telefonbuch aus: %d", st)
	}
	e.pb.setDown(false)
	// In UCNG gesperrtes Konto kommt auch über das Telefonbuch nicht hinein
	if _, err := e.users.AdminMember(id, "disable"); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.exchange(t, c); res.StatusCode != 403 {
		t.Fatalf("gesperrtes Konto: %d", res.StatusCode)
	}
	if c := status(t, "GET", base+"/api/session", tok, ""); c != 401 {
		t.Fatalf("Sitzung nach Sperre: %d", c)
	}
}

// openStream öffnet den Live-Stream und meldet, wann er endet.
func openStream(t *testing.T, base, room, viewer string) <-chan struct{} {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/rooms/"+room+"/events?viewer="+viewer, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("Stream: %v %v", err, res)
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
		}
		res.Body.Close()
		close(done)
	}()
	return done
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: Stream bleibt offen", what)
	}
}

func TestPhonebookRevoke(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	c := claimsFor("p_carl", "cmeier", "Carl Meier", "")
	res, out := e.exchange(t, c)
	if res.StatusCode != 200 {
		t.Fatal(out)
	}
	s := out["session"].(map[string]any)
	tok, viewer, id := s["token"].(string), s["viewer"].(string), s["id"].(string)
	c2 := claimsFor("p_carl", "cmeier", "Carl Meier", "")
	tok2, _ := e.signIn(t, c2) // zweites Gerät, andere Telefonbuch-Sitzung
	// ohne bzw. mit falschem Schlüssel
	body := jsonBody(map[string]any{"sessions": []string{c.Session}})
	for _, key := range []string{"", "falsch", e.pb.cfg.key(pbKeyUCNGToPhonebook)} {
		if st := status(t, "POST", base+"/api/integration/phonebook/revoke", key, body); st != 401 {
			t.Fatalf("Widerruf mit Schlüssel %q: %d", key, st)
		}
	}
	stream := openStream(t, base, "lobby", viewer)
	if e.srv.voice != nil {
		if _, ok := e.srv.voice.whoByID(id); !ok {
			t.Fatal("Sprachkanal: Person vor dem Widerruf unbekannt")
		}
	}
	res = do(t, "POST", base+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), body)
	if out := jmap(t, res); res.StatusCode != 200 || out["ended"] != float64(1) {
		t.Fatalf("Widerruf: %d %v", res.StatusCode, out)
	}
	waitClosed(t, stream, "Abmeldung im Telefonbuch")
	if st := status(t, "GET", base+"/api/session", tok, ""); st != 401 {
		t.Fatalf("widerrufene Sitzung: %d", st)
	}
	if st := status(t, "GET", base+"/api/session", tok2, ""); st != 200 {
		t.Fatalf("andere Sitzung mitbeendet: %d", st)
	}
	// Sperre der Person: alle Sitzungen
	body = jsonBody(map[string]any{"subjects": []string{"p_carl"}})
	do(t, "POST", base+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), body).Body.Close()
	if st := status(t, "GET", base+"/api/session", tok2, ""); st != 401 {
		t.Fatalf("Sitzung nach Sperre: %d", st)
	}
	if e.srv.voice != nil {
		if _, ok := e.srv.voice.whoByID(id); ok {
			t.Fatal("Sprachkanal: Person nach Sperre noch zugelassen")
		}
	}
}

func TestPhonebookRevalidate(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	ctx := context.Background()
	c := claimsFor("p_dora", "dkrause", "Dora Krause", "")
	res, out := e.exchange(t, c)
	if res.StatusCode != 200 {
		t.Fatal(out)
	}
	s := out["session"].(map[string]any)
	tok, viewer := s["token"].(string), s["viewer"].(string)
	ok := func(want int, what string) {
		t.Helper()
		if st := status(t, "GET", base+"/api/session", tok, ""); st != want {
			t.Fatalf("%s: %d (erwartet %d)", what, st, want)
		}
	}
	if err := e.srv.pbRevalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.pb.active[c.Session] {
		t.Fatal("Nutzung nicht als Aktivität gemeldet")
	}
	// Telefonbuch fällt aus: innerhalb der Frist weiter gültig, danach nicht mehr (fail-closed)
	e.pb.setDown(true)
	e.clock.add(10 * time.Minute)
	if err := e.srv.pbRevalidate(ctx); err == nil {
		t.Fatal("Ausfall nicht erkannt")
	}
	ok(200, "innerhalb der Frist")
	stream := openStream(t, base, "lobby", viewer)
	e.clock.add(6 * time.Minute)
	// gesperrt (fail-closed), aber als „Telefonbuch nicht erreichbar“ (503) – nicht „abgemeldet“
	// (401), damit die Oberfläche das Token behält (PH-04)
	ok(503, "nach Ablauf der Frist")
	for _, c := range []struct{ m, p, body string }{
		{"GET", "/api/rooms", ""}, {"GET", "/api/rooms/lobby/messages?limit=5", ""}, {"GET", "/api/account", ""},
		{"POST", "/api/rooms/lobby/messages", `{"text":"während des Ausfalls"}`},
		{"GET", "/api/integration/phonebook/user?subject=p_dora", ""},
	} {
		res := do(t, c.m, base+c.p, tok, c.body)
		got := jmap(t, res)
		if res.StatusCode != 503 || got["phonebookDown"] != true || res.Header.Get("Retry-After") == "" {
			t.Fatalf("%s %s während des Ausfalls: %d %v", c.m, c.p, res.StatusCode, got)
		}
	}
	if msgs, _, _ := e.hub.History("lobby", 0, 50); len(msgs) != 0 {
		t.Fatalf("Nachricht trotz Ausfall angenommen: %d", len(msgs))
	}
	e.users.ExtSweep()
	waitClosed(t, stream, "Frist abgelaufen")
	// Telefonbuch wieder da: Sitzung gilt wieder (nichts gelöscht)
	e.pb.setDown(false)
	if err := e.srv.pbRevalidate(ctx); err != nil {
		t.Fatal(err)
	}
	ok(200, "nach Wiederherstellung")
	// Rolle und Name ändern sich im Telefonbuch
	cc := c
	cc.Role, cc.SessionExpires = RoleModerator, e.clock.now().Add(8*time.Hour)
	e.pb.set(c.Session, pbResult{Valid: true, Claims: &cc})
	e.srv.pbRevalidate(ctx)
	if acc := jmap(t, do(t, "GET", base+"/api/account", tok, "")); acc["external"].(map[string]any)["role"] != RoleModerator {
		t.Fatalf("Rolle nicht übernommen: %v", acc["external"])
	}
	// Telefonbuch-Sitzung abgelaufen (Until)
	cc.SessionExpires = e.clock.now().Add(time.Minute)
	e.pb.set(c.Session, pbResult{Valid: true, Claims: &cc})
	e.srv.pbRevalidate(ctx)
	e.clock.add(2 * time.Minute)
	ok(401, "nach Ablauf der Telefonbuch-Sitzung")
	e.pb.setDown(true) // abgelaufen bleibt abgelaufen, auch wenn zusätzlich die Frist verstreicht
	e.clock.add(30 * time.Minute)
	ok(401, "abgelaufen und Telefonbuch nicht erreichbar")
	e.pb.setDown(false)
	// Sperre im AD/Telefonbuch: alle Sitzungen weg
	tokB, _ := e.signIn(t, claimsFor("p_dora", "dkrause", "Dora Krause", ""))
	other := claimsFor("p_dora", "dkrause", "Dora Krause", "")
	tokC, _ := e.signIn(t, other)
	e.pb.set(other.Session, pbResult{Valid: false, Reason: "disabled"})
	e.srv.pbRevalidate(ctx)
	for _, x := range []string{tokB, tokC} {
		if st := status(t, "GET", base+"/api/session", x, ""); st != 401 {
			t.Fatalf("Sitzung nach Sperre: %d", st)
		}
	}
	// Antwort für ein anderes Subjekt wird nicht übernommen (Verwechslung → ungültig)
	tokD, _ := e.signIn(t, claimsFor("p_emil", "ewolf", "Emil Wolf", ""))
	e.users.mu.Lock()
	x := e.users.byExt[extKey(pbProvider, "p_emil")]
	ref := x.Member.Sessions[0].PB
	e.users.mu.Unlock()
	wrong := claimsFor("p_dora", "dkrause", "Dora Krause", "")
	e.pb.set(ref, pbResult{Valid: true, Claims: &wrong})
	e.srv.pbRevalidate(ctx)
	if st := status(t, "GET", base+"/api/session", tokD, ""); st != 401 {
		t.Fatalf("Antwort mit fremdem Subjekt übernommen: %d", st)
	}
}

func TestPhonebookAdminSSO(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	ctx := context.Background()
	admin := func(tok string) (int, string) {
		res := do(t, "POST", base+"/api/integration/phonebook/admin", tok, "")
		out := jmap(t, res)
		s, _ := out["token"].(string)
		return res.StatusCode, s
	}
	if st, _ := admin(""); st != 401 {
		t.Fatalf("ohne Anmeldung: %d", st)
	}
	user, _ := e.signIn(t, claimsFor("p_u", "user", "Nutzer", ""))
	if st, _ := admin(user); st != 403 {
		t.Fatalf("ohne Zuordnung: %d", st)
	}
	mc := claimsFor("p_m", "mod", "Moderatorin", RoleModerator)
	modTok, _ := e.signIn(t, mc)
	st, mAdm := admin(modTok)
	if st != 200 {
		t.Fatalf("Moderator: %d", st)
	}
	if c := status(t, "GET", base+"/api/admin/reports", mAdm, ""); c != 200 {
		t.Fatalf("Moderator darf moderieren: %d", c)
	}
	if c := status(t, "PUT", base+"/api/admin/settings", mAdm, "{}"); c != 403 {
		t.Fatalf("Moderator darf Einstellungen ändern: %d", c)
	}
	ac := claimsFor("p_a", "chefin", "Chefin", RoleAdmin)
	admTok, _ := e.signIn(t, ac)
	_, aAdm := admin(admTok)
	me := jmap(t, do(t, "GET", base+"/api/admin/me", aAdm, ""))
	if a, _ := me["actor"].(map[string]any); a == nil || a["role"] != RoleAdmin || a["name"] != "pb:chefin" || a["display"] != "Chefin" {
		t.Fatalf("Admin: %v", me)
	}
	// Zuordnung entfällt im Telefonbuch → Admin-Sitzung endet bei der nächsten Prüfung
	nc := ac
	nc.Role = ""
	e.pb.set(ac.Session, pbResult{Valid: true, Claims: &nc})
	e.srv.pbRevalidate(ctx)
	if c := status(t, "GET", base+"/api/admin/me", aAdm, ""); c != 401 {
		t.Fatalf("Admin-Sitzung nach Entzug der Rolle: %d", c)
	}
	// Abmeldung im Telefonbuch beendet auch die Admin-Sitzung des Moderators
	do(t, "POST", base+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"sessions": []string{mc.Session}})).Body.Close()
	if c := status(t, "GET", base+"/api/admin/me", mAdm, ""); c != 401 {
		t.Fatalf("Admin-Sitzung nach Abmeldung: %d", c)
	}
	// Notfallzugang bleibt: Haupt-Admin mit ADMIN_PASSWORD
	if c := status(t, "POST", base+"/api/admin/login", "", jsonBody(map[string]string{"password": testAdminPW})); c != 200 {
		t.Fatalf("Notfallzugang: %d", c)
	}
}

func pbE2EMsg(spk, rpk string) string {
	b, _ := json.Marshal(map[string]any{"v": 2, "iv": b64(rnd(12)), "ct": b64(rnd(40)), "spk": spk, "rpk": rpk})
	return string(b)
}

func recMat() RecoveryMaterial {
	return RecoveryMaterial{Auth: b64(rnd(32)), KDFSalt: b64(rnd(16)), KDFIter: 100_000, PrivKey: b64(rnd(60))}
}

func TestPhonebookE2EE(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	ta, _ := e.signIn(t, claimsFor("p_a", "anna", "Anna", ""))
	tb, idB := e.signIn(t, claimsFor("p_b", "bert", "Bert", ""))
	act := func(tok, action string, body any) (int, map[string]any) {
		t.Helper()
		res := do(t, "POST", base+"/api/account/e2ee/"+action, tok, jsonBody(body))
		return res.StatusCode, jmap(t, res)
	}
	var dm DMInfo
	json.NewDecoder(do(t, "POST", base+"/api/dms", ta, jsonBody(map[string]string{"userId": idB})).Body).Decode(&dm)
	if dm.Room == "" {
		t.Fatal("keine Direktnachricht")
	}
	// ohne Schlüssel: keine unverschlüsselten Direktnachrichten
	if c := status(t, "POST", base+"/api/rooms/"+dm.Room+"/messages", ta, `{"text":"hallo"}`); c != 409 {
		t.Fatalf("Klartext-DM: %d", c)
	}
	ka, kb := testKeys(), testKeys()
	if c, out := act(ta, "setup", map[string]any{"keys": ka, "recovery": recMat()}); c != 200 {
		t.Fatalf("Einrichten: %d %v", c, out)
	}
	if c, _ := act(ta, "setup", map[string]any{"keys": testKeys(), "recovery": recMat()}); c != 409 {
		t.Fatalf("zweites Einrichten überschreibt Schlüssel: %d", c)
	}
	if c := status(t, "POST", base+"/api/rooms/"+dm.Room+"/messages", ta, `{"text":"hallo"}`); c != 409 {
		t.Fatalf("Klartext-DM, Gegenüber ohne Schlüssel: %d", c)
	}
	// auch keine Bilder in Direktnachrichten (die lägen unverschlüsselt auf dem Server)
	if out := jmap(t, do(t, "POST", base+"/api/rooms/"+dm.Room+"/upload", ta, "")); out["error"] != ErrE2EMedia.Error() {
		t.Fatalf("Upload in DM: %v", out)
	}
	act(tb, "setup", map[string]any{"keys": kb, "recovery": recMat()})
	if c := status(t, "POST", base+"/api/rooms/"+dm.Room+"/messages", ta, `{"text":"hallo"}`); c == 201 {
		t.Fatal("Klartext-DM trotz Verschlüsselung angenommen")
	}
	if c := status(t, "POST", base+"/api/rooms/"+dm.Room+"/messages", ta, jsonBody(map[string]string{"e2e": pbE2EMsg(ka.PubKey, kb.PubKey)})); c != 201 {
		t.Fatalf("verschlüsselte DM: %d", c)
	}
	acc := jmap(t, do(t, "GET", base+"/api/account", ta, ""))
	if acc["pubKey"] != ka.PubKey || acc["privKey"] != ka.PrivKey || acc["kdfSalt"] != ka.KDFSalt || acc["e2eRecovery"] != true {
		t.Fatalf("Konto: %v", acc)
	}
	// Geheimnis ändern: nur mit dem bisherigen, gleicher öffentlicher Schlüssel
	nk := testKeys()
	nk.PubKey = ka.PubKey
	if c, _ := act(ta, "secret", map[string]any{"auth": b64(rnd(32)), "keys": nk}); c != 401 {
		t.Fatalf("falsches Geheimnis: %d", c)
	}
	other := testKeys()
	if c, _ := act(ta, "secret", map[string]any{"auth": ka.Auth, "keys": other}); c != 409 {
		t.Fatalf("Schlüsseltausch über Geheimnis-Wechsel: %d", c)
	}
	if c, _ := act(ta, "secret", map[string]any{"auth": ka.Auth, "keys": nk}); c != 200 {
		t.Fatalf("Geheimnis ändern: %d", c)
	}
	// Wiederherstellung: Kopie abrufen, mit falschem Code abgelehnt, mit richtigem neues Geheimnis
	rec := recMat()
	if c, _ := act(ta, "recovery", map[string]any{"auth": ka.Auth, "recovery": rec}); c != 401 {
		t.Fatalf("neuer Code mit altem Geheimnis: %d", c)
	}
	if c, _ := act(ta, "recovery", map[string]any{"auth": nk.Auth, "recovery": rec}); c != 200 {
		t.Fatalf("neuer Code: %d", c)
	}
	blob := jmap(t, do(t, "GET", base+"/api/account/e2ee/recovery", ta, ""))
	if blob["privKey"] != rec.PrivKey || blob["kdfSalt"] != rec.KDFSalt || blob["auth"] != "" {
		t.Fatalf("Kopie: %v", blob)
	}
	k3 := testKeys()
	k3.PubKey = ka.PubKey
	if c, _ := act(ta, "recover", map[string]any{"recoveryAuth": b64(rnd(32)), "keys": k3, "recovery": recMat()}); c != 401 {
		t.Fatalf("falscher Wiederherstellungscode: %d", c)
	}
	rec2 := recMat()
	if c, _ := act(ta, "recover", map[string]any{"recoveryAuth": rec.Auth, "keys": k3, "recovery": rec2}); c != 200 {
		t.Fatalf("Wiederherstellen: %d", c)
	}
	if c, _ := act(ta, "recover", map[string]any{"recoveryAuth": rec.Auth, "keys": k3, "recovery": recMat()}); c != 401 {
		t.Fatalf("Wiederherstellungscode zweimal benutzbar: %d", c)
	}
	// Zurücksetzen nur kurz nach frischer Anmeldung
	e.clock.add(20 * time.Minute)
	e.srv.pbRevalidate(context.Background())
	if c, out := act(ta, "reset", nil); c != 403 || out["reauth"] != true {
		t.Fatalf("Zurücksetzen mit alter Sitzung: %d %v", c, out)
	}
	fresh, _ := e.signIn(t, claimsFor("p_a", "anna", "Anna", ""))
	if c, _ := act(fresh, "reset", nil); c != 200 {
		t.Fatalf("Zurücksetzen: %d", c)
	}
	if acc := jmap(t, do(t, "GET", base+"/api/account", fresh, "")); acc["pubKey"] != nil {
		t.Fatalf("Schlüssel nach Zurücksetzen: %v", acc["pubKey"])
	}
}

// Konten und Sitzungen aus der Zeit als hosted (Passwort-Anmeldung) gelten im Modus phonebook nicht.
func TestPhonebookLegacySessions(t *testing.T) {
	e := phonebookServer(t)
	k := testKeys()
	if _, _, _, err := e.users.Register(RegisterInput{Name: "lokal", Keys: k}, false); err != nil {
		t.Fatal(err)
	}
	res, err := e.users.Login(LoginInput{Login: "lokal", Auth: k.Auth}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/session", "/api/account", "/api/rooms"} {
		if c := status(t, "GET", e.ts.URL+p, res.Token, ""); c != 401 {
			t.Errorf("alte Sitzung %s: %d", p, c)
		}
	}
	if c := status(t, "POST", e.ts.URL+"/api/account/e2ee/setup", res.Token, jsonBody(map[string]any{"keys": testKeys(), "recovery": recMat()})); c != 401 {
		t.Errorf("alte Sitzung E2EE: %d", c)
	}
}
