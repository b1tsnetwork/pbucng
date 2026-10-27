package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// revokeSink nimmt Widerrufe entgegen wie UCNG.
type revokeSink struct {
	mu   sync.Mutex
	key  string
	got  []commRevokeReq
	orgs []orgPush
	bad  int
	fail bool
	news int // PH-05: Anfragen an …/news
}

func (r *revokeSink) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.Method == http.MethodPut && req.URL.Path == "/api/integration/phonebook/org" && req.Header.Get("Authorization") == "Bearer "+r.key {
		var in orgPush
		json.NewDecoder(req.Body).Decode(&in)
		r.orgs = append(r.orgs, in)
		w.Write([]byte(`{"created":[],"updated":[],"archived":[]}`))
		return
	}
	if req.Method == http.MethodPost && req.URL.Path == "/api/integration/phonebook/news" && req.Header.Get("Authorization") == "Bearer "+r.key {
		var in struct{ Subject string }
		json.NewDecoder(req.Body).Decode(&in)
		r.news++
		if !strings.HasPrefix(in.Subject, "p_") {
			http.Error(w, "kein Principal", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"dms":2,"mentions":1,"since":"2026-10-27T08:00:00Z"}`))
		return
	}
	if req.URL.Path != "/api/integration/phonebook/revoke" || req.Header.Get("Authorization") != "Bearer "+r.key {
		r.bad++
		http.Error(w, "nein", http.StatusUnauthorized)
		return
	}
	if r.fail {
		http.Error(w, "kaputt", http.StatusServiceUnavailable)
		return
	}
	var in commRevokeReq
	json.NewDecoder(req.Body).Decode(&in)
	r.got = append(r.got, in)
	w.Write([]byte(`{"ended":1}`))
}

func (r *revokeSink) wait(t *testing.T, n int) []commRevokeReq {
	t.Helper()
	for i := 0; i < 100; i++ {
		r.mu.Lock()
		if len(r.got) >= n {
			out := append([]commRevokeReq(nil), r.got...)
			r.mu.Unlock()
			return out
		}
		r.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Widerruf kam nicht an")
	return nil
}

type orgPush struct {
	Version string     `json:"version"`
	Units   []commUnit `json:"units"`
}

type ssoEnv struct {
	a    *App
	h    http.Handler
	sink *revokeSink
}

func ssoApp(t *testing.T, mod func(*CommunicationConfig)) *ssoEnv {
	t.Helper()
	sink := &revokeSink{}
	ucng := httptest.NewServer(sink)
	t.Cleanup(ucng.Close)
	a := testApp(t)
	a.cfg.PublicURL = "https://tb.firma.local"
	a.cfg.Communication = commConfig(t)
	a.cfg.Communication.InternalBaseURL = ucng.URL
	if mod != nil {
		mod(&a.cfg.Communication)
	}
	if err := a.cfg.Communication.validate(a.cfg.PublicURL, false); err != nil {
		t.Fatal(err)
	}
	sink.key = a.cfg.Communication.key(commKeyToUCNG)
	a.commOut = make(chan commRevokeReq, 10)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.commRevokeLoop(ctx)
	a.comm.mu.Lock()
	a.comm.checkedAt, a.comm.reachable, a.comm.secure = time.Now(), true, true
	a.comm.mu.Unlock()
	for _, u := range []struct {
		name  string
		admin bool
	}{{"anna", false}, {"bert", false}, {"chef", true}} {
		h, _ := hashPassword("passwort-" + u.name)
		if err := a.st().UpsertLocalUser(context.Background(), LocalUser{Username: u.name, DisplayName: strings.ToUpper(u.name[:1]) + u.name[1:], PwHash: h, IsAdmin: u.admin, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	return &ssoEnv{a: a, h: a.routes(false), sink: sink}
}

// login meldet über /api/login an und liefert Cookie und CSRF-Token.
func (e *ssoEnv) login(t *testing.T, user string) (*http.Cookie, string) {
	t.Helper()
	rec := doReq(e.h, "POST", "/api/login", `{"username":"`+user+`","password":"passwort-`+user+`"}`, "10.0.0.5", nil)
	if rec.Code != 200 {
		t.Fatalf("Anmeldung %s: %d %s", user, rec.Code, rec.Body)
	}
	c := rec.Result().Cookies()[0]
	me := getWith(e.h, "/api/me", c, "10.0.0.5")
	var out map[string]any
	json.Unmarshal(me.Body.Bytes(), &out)
	return c, out["csrf"].(string)
}

func pkce() (verifier, challenge string) {
	verifier = randomToken(32)
	return verifier, s256(verifier)
}

// authorize ruft die Anmeldeseite auf und liefert das Ziel der Weiterleitung.
func (e *ssoEnv) authorize(t *testing.T, c *http.Cookie, state, challenge, extra string) (int, *url.URL) {
	t.Helper()
	rec := getWith(e.h, "/api/addons/communication/authorize?state="+state+"&challenge="+challenge+"&method=S256"+extra, c, "10.0.0.5")
	if rec.Code != http.StatusSeeOther {
		return rec.Code, nil
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Kopfzeilen: %v", rec.Header())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return rec.Code, u
}

func frag(u *url.URL) url.Values {
	v, _ := url.ParseQuery(u.Fragment)
	return v
}

func (e *ssoEnv) internal(path, key, body string) *httptest.ResponseRecorder {
	return doReq(e.h, "POST", path, body, "172.18.0.9", map[string]string{"Authorization": "Bearer " + key})
}

func (e *ssoEnv) redeem(code, verifier string) (int, commClaims) {
	rec := e.internal("/api/internal/communication/redeem", e.a.cfg.Communication.key(commKeyFromUCNG), `{"code":"`+code+`","verifier":"`+verifier+`"}`)
	var c commClaims
	json.Unmarshal(rec.Body.Bytes(), &c)
	return rec.Code, c
}

// codeFor: kompletter Browserteil bis zum Code
func (e *ssoEnv) codeFor(t *testing.T, c *http.Cookie) (code, verifier string) {
	t.Helper()
	verifier, ch := pkce()
	state := randomToken(16)
	st, u := e.authorize(t, c, state, ch, "")
	if st != 303 || u == nil {
		t.Fatalf("authorize: %d", st)
	}
	f := frag(u)
	if f.Get("pb_state") != state || f.Get("pb_code") == "" {
		t.Fatalf("Rücksprung: %s", u)
	}
	return f.Get("pb_code"), verifier
}

func TestCommSSOAuthorize(t *testing.T) {
	e := ssoApp(t, nil)
	state := randomToken(16)
	_, ch := pkce()
	// ungültige Anfragen
	for _, q := range []string{"state=kurz&challenge=" + ch + "&method=S256", "state=" + state + "&challenge=abc&method=S256", "state=" + state + "&challenge=" + ch + "&method=plain", "state=" + state + "&challenge=" + ch} {
		if rec := getWith(e.h, "/api/addons/communication/authorize?"+q, nil, "10.0.0.5"); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	// nicht angemeldet (auch Gast bei öffentlichem Telefonbuch): zur Anmeldung, danach weiter
	e.a.cfg.Auth.PublicDirectory = true
	st, u := e.authorize(t, nil, state, ch, "")
	if st != 303 || u.Path != "/" || u.Query().Get("commState") != state || u.Query().Get("commChallenge") != ch || u.Host != "" {
		t.Fatalf("ohne Anmeldung: %d %v", st, u)
	}
	c, _ := e.login(t, "anna")
	st, u = e.authorize(t, c, state, ch, "")
	if st != 303 || u.Scheme+"://"+u.Host != "https://kommunikation.firma.local" || u.RawQuery != "" {
		t.Fatalf("Rücksprung: %v", u)
	}
	f := frag(u)
	if f.Get("pb_state") != state || len(f.Get("pb_code")) < 40 || f.Get("pb_error") != "" {
		t.Fatalf("Fragment: %v", f)
	}
	// DEV-Admin: kein Zugang
	dev := httptest.NewRecorder()
	e.a.createSession(context.Background(), dev, httptest.NewRequest("POST", "/api/login", nil), "devadmin", "DEV", true, "dev", "")
	if _, u := e.authorize(t, dev.Result().Cookies()[0], state, ch, ""); frag(u).Get("pb_error") != "forbidden" || frag(u).Get("pb_code") != "" {
		t.Fatalf("DEV-Admin: %v", u)
	}
	// Sitzung von vor dem Einschalten (ohne Principal): neu anmelden – aber nicht im Kreis
	old := sessionCookie(t, e.a, "bert", false)
	if _, u := e.authorize(t, old, state, ch, ""); u.Path != "/" || u.Query().Get("commState") != state {
		t.Fatalf("alte Sitzung: %v", u)
	}
	if rec := getWith(e.h, "/api/me", old, "10.0.0.5"); rec.Code != 401 {
		t.Fatalf("alte Sitzung nicht beendet: %d", rec.Code)
	}
	old = sessionCookie(t, e.a, "bert", false)
	if _, u := e.authorize(t, old, state, ch, "&relogin=1"); frag(u).Get("pb_error") != "unavailable" {
		t.Fatalf("Schleifenschutz: %v", u)
	}
	// UCNG unsicher bzw. nicht geprüft: keine Codes
	e.a.comm.mu.Lock()
	e.a.comm.secure = false
	e.a.comm.mu.Unlock()
	if _, u := e.authorize(t, c, state, ch, ""); frag(u).Get("pb_error") != "insecure" || frag(u).Get("pb_code") != "" {
		t.Fatalf("unsicheres UCNG: %v", u)
	}
	e.a.comm.mu.Lock()
	e.a.comm.reachable = false
	e.a.comm.mu.Unlock()
	if _, u := e.authorize(t, c, state, ch, ""); frag(u).Get("pb_error") != "unavailable" {
		t.Fatalf("UCNG weg: %v", u)
	}
}

func TestCommSSORedeem(t *testing.T) {
	e := ssoApp(t, nil)
	c, _ := e.login(t, "anna")
	code, ver := e.codeFor(t, c)
	// falscher bzw. vertauschter Schlüssel
	for _, key := range []string{"", "x", e.a.cfg.Communication.key(commKeyToUCNG)} {
		if rec := e.internal("/api/internal/communication/redeem", key, `{"code":"`+code+`","verifier":"`+ver+`"}`); rec.Code != 401 {
			t.Fatalf("Schlüssel %q: %d", key, rec.Code)
		}
	}
	st, cl := e.redeem(code, ver)
	if st != 200 || !strings.HasPrefix(cl.Subject, "p_") || cl.Username != "anna" || cl.DisplayName != "Anna" || cl.Role != "" || len(cl.Session) != 64 || cl.SessionExpires.IsZero() {
		t.Fatalf("Einlösen: %d %+v", st, cl)
	}
	// einmalig
	if st, _ := e.redeem(code, ver); st != 400 {
		t.Fatalf("zweites Einlösen: %d", st)
	}
	// falscher verifier verbraucht den Code
	code, ver = e.codeFor(t, c)
	if st, _ := e.redeem(code, randomToken(32)); st != 400 {
		t.Fatalf("falscher verifier: %d", st)
	}
	if st, _ := e.redeem(code, ver); st != 400 {
		t.Fatalf("Code nach falschem verifier: %d", st)
	}
	// abgelaufen
	code, ver = e.codeFor(t, c)
	if _, ok := e.a.commCodes.take(code, time.Now().Add(61*time.Second)); ok {
		t.Fatal("abgelaufener Code angenommen")
	}
	if st, _ := e.redeem(code, ver); st != 400 {
		t.Fatalf("abgelaufener Code: %d", st)
	}
	// gleiche Person, zweites Mal: gleiche principal_id
	code, ver = e.codeFor(t, c)
	if _, cl2 := e.redeem(code, ver); cl2.Subject != cl.Subject {
		t.Fatalf("Subjekt wechselt: %s / %s", cl.Subject, cl2.Subject)
	}
	// Abmeldung zwischen authorize und Einlösen
	code, ver = e.codeFor(t, c)
	c2, csrf := e.login(t, "anna")
	_ = c2
	code2, ver2 := e.codeFor(t, c2)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/logout", nil)
	req.AddCookie(c2)
	req.Header.Set("X-CSRF-Token", csrf)
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("Abmelden: %d", rec.Code)
	}
	if st, _ := e.redeem(code2, ver2); st != 400 {
		t.Fatalf("Code nach Abmeldung: %d", st)
	}
	if st, _ := e.redeem(code, ver); st != 200 { // andere Sitzung bleibt
		t.Fatalf("andere Sitzung: %d", st)
	}
	got := e.sink.wait(t, 1)
	if len(got[0].Sessions) != 1 || len(got[0].Sessions[0]) != 64 {
		t.Fatalf("Widerruf: %+v", got)
	}
	// lokales Konto ohne Telefonbucheintrag ist kein Ausschlussgrund (anna hat keinen Eintrag)
	if id, _ := e.a.st().EmployeeIDByUsername(context.Background(), "anna"); id != 0 {
		t.Fatalf("Testvoraussetzung: anna hat einen Eintrag")
	}
}

func TestCommSSOAccessAndRoles(t *testing.T) {
	e := ssoApp(t, func(c *CommunicationConfig) {
		c.Access.Users = []string{"Anna"}
		c.Roles.ModeratorUsers = []string{"bert"}
	})
	ca, _ := e.login(t, "anna")
	cb, _ := e.login(t, "bert")
	cc, _ := e.login(t, "chef")
	code, ver := e.codeFor(t, ca)
	if _, cl := e.redeem(code, ver); cl.Role != "" {
		t.Fatalf("anna: Rolle %q", cl.Role)
	}
	// Moderator ist automatisch nutzungsberechtigt
	code, ver = e.codeFor(t, cb)
	if _, cl := e.redeem(code, ver); cl.Role != commRoleMod {
		t.Fatalf("bert: Rolle %q", cl.Role)
	}
	// Telefonbuch-Admin ohne Zuordnung: kein Zugang (nicht in access), keine Admin-Rolle
	_, ch := pkce()
	if _, u := e.authorize(t, cc, randomToken(16), ch, ""); frag(u).Get("pb_error") != "forbidden" {
		t.Fatalf("chef ohne Zuordnung: %v", u)
	}
	me := getWith(e.h, "/api/me", cc, "10.0.0.5")
	var out map[string]any
	json.Unmarshal(me.Body.Bytes(), &out)
	if cm, _ := out["communication"].(map[string]any); cm == nil || cm["allowed"] != false || cm["url"] != "https://kommunikation.firma.local" {
		t.Fatalf("/api/me: %v", out["communication"])
	}
	// Inbetriebnahme-Schalter: Telefonbuch-Admins werden UCNG-Admins
	e.a.cfg.Communication.Roles.PhonebookAdminsAreUCNGAdmins = true
	e.a.commCfgInvalidate() // wirksame Einstellungen sind 30 s gepuffert
	code, ver = e.codeFor(t, cc)
	if _, cl := e.redeem(code, ver); cl.Role != commRoleAdmin {
		t.Fatalf("Bootstrap: Rolle %q", cl.Role)
	}
	// Add-on aus: kein Eintrag in /api/me
	a := testApp(t)
	if _, ok := a.meJSON(&Session{Username: "x"})["communication"]; ok {
		t.Fatal("communication in /api/me ohne Add-on")
	}
}

type failingStore struct {
	Store
	fail bool
}

func (f *failingStore) GetSession(ctx context.Context, h string) (*SessionRec, error) {
	if f.fail {
		return nil, errors.New("Datenbank weg")
	}
	return f.Store.GetSession(ctx, h)
}

func TestCommSSOValidate(t *testing.T) {
	e := ssoApp(t, nil)
	ctx := context.Background()
	c, csrf := e.login(t, "anna")
	code, ver := e.codeFor(t, c)
	_, cl := e.redeem(code, ver)
	cb, _ := e.login(t, "bert")
	code, ver = e.codeFor(t, cb)
	_, clb := e.redeem(code, ver)
	key := e.a.cfg.Communication.key(commKeyFromUCNG)
	validate := func(items ...commCheck) map[string]commResult {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"sessions": items})
		rec := e.internal("/api/internal/communication/validate", key, string(b))
		if rec.Code != 200 {
			t.Fatalf("validate: %d %s", rec.Code, rec.Body)
		}
		var out struct{ Results []commResult }
		json.Unmarshal(rec.Body.Bytes(), &out)
		m := map[string]commResult{}
		for _, r := range out.Results {
			m[r.Session] = r
		}
		return m
	}
	if rec := e.internal("/api/internal/communication/validate", e.a.cfg.Communication.key(commKeyToUCNG), `{"sessions":[]}`); rec.Code != 401 {
		t.Fatalf("falscher Schlüssel: %d", rec.Code)
	}
	r := validate(commCheck{Session: cl.Session, Subject: cl.Subject, Active: true}, commCheck{Session: "0000", Subject: cl.Subject},
		commCheck{Session: clb.Session, Subject: cl.Subject})
	if !r[cl.Session].Valid || r[cl.Session].Claims == nil || r[cl.Session].Claims.Subject != cl.Subject {
		t.Fatalf("gültige Sitzung: %+v", r[cl.Session])
	}
	if r["0000"].Valid || r["0000"].Reason != "logout" {
		t.Fatalf("unbekannte Sitzung: %+v", r["0000"])
	}
	if r[clb.Session].Valid { // Sitzung gehört einer anderen Person
		t.Fatalf("fremdes Subjekt: %+v", r[clb.Session])
	}
	// Datenbank nicht erreichbar: keine Antwort (UCNG lässt seine Frist laufen, fail-closed)
	fs := &failingStore{Store: e.a.st(), fail: true}
	e.a.setStore(fs)
	b, _ := json.Marshal(map[string]any{"sessions": []commCheck{{Session: cl.Session, Subject: cl.Subject}}})
	if rec := e.internal("/api/internal/communication/validate", key, string(b)); rec.Code != 503 {
		t.Fatalf("Datenbank weg: %d", rec.Code)
	}
	e.a.setStore(fs.Store)
	// Abmeldung → ungültig
	req := httptest.NewRequest("POST", "/api/logout", nil)
	req.AddCookie(c)
	req.Header.Set("X-CSRF-Token", csrf)
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	if r := validate(commCheck{Session: cl.Session, Subject: cl.Subject}); r[cl.Session].Valid || r[cl.Session].Reason != "logout" {
		t.Fatalf("nach Abmeldung: %+v", r[cl.Session])
	}
	// Konto gesperrt (lokal) → bei der nächsten Nachprüfung disabled, Telefonbuch-Sitzungen weg, Widerruf an UCNG
	lu, _ := e.a.st().GetLocalUser(ctx, "bert")
	lu.Disabled = true
	e.a.st().UpsertLocalUser(ctx, *lu)
	if r := validate(commCheck{Session: clb.Session, Subject: clb.Subject}); !r[clb.Session].Valid {
		t.Fatalf("vor Ablauf von adRecheck schon geprüft: %+v", r[clb.Session])
	}
	p, _ := e.a.st().GetPrincipal(ctx, clb.Subject)
	p.CheckedAt = time.Now().Add(-20 * time.Minute)
	e.a.st().UpsertPrincipal(ctx, p)
	if r := validate(commCheck{Session: clb.Session, Subject: clb.Subject}); r[clb.Session].Valid || r[clb.Session].Reason != "logout" && r[clb.Session].Reason != "disabled" {
		t.Fatalf("gesperrt: %+v", r[clb.Session])
	}
	if rec := getWith(e.h, "/api/me", cb, "10.0.0.5"); rec.Code != 401 {
		t.Fatalf("Telefonbuch-Sitzung nach Sperre: %d", rec.Code)
	}
	found := false
	for _, g := range e.sink.wait(t, 2) {
		if len(g.Subjects) == 1 && g.Subjects[0] == clb.Subject {
			found = true
		}
	}
	if !found {
		t.Fatalf("kein Widerruf der Person: %+v", e.sink.got)
	}
}

// Rückfall-Betrieb (Datenbank nur lesend): Anmeldung am Telefonbuch geht, Kommunikation sagt
// „nicht verfügbar“ statt im Kreis zu schicken.
func TestCommSSOOffline(t *testing.T) {
	e := ssoApp(t, nil)
	ro := &readOnlyStore{e.a.st().(*jsonStore)}
	e.a.setStore(ro)
	e.a.offline.Store(true)
	c, _ := e.login(t, "anna")
	_, ch := pkce()
	if _, u := e.authorize(t, c, randomToken(16), ch, ""); frag(u).Get("pb_error") != "unavailable" {
		t.Fatalf("offline: %v", u)
	}
}

func TestCommSSOKeysDiffer(t *testing.T) {
	c := CommunicationConfig{secret: strings.Repeat("k", 40)}
	if c.key(commKeyFromUCNG) == c.key(commKeyToUCNG) {
		t.Fatal("gleicher Schlüssel für beide Richtungen")
	}
	// gleiche Ableitung wie in UCNG (phonebook.go): HMAC-SHA256(secret, "ucng-phonebook-v1|"+Richtung), hex
	if got := c.key("ucng->phonebook"); len(got) != 64 {
		t.Fatal(got)
	}
	if s256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" { // RFC 7636, Anhang B
		t.Fatal("S256 falsch")
	}
	_ = base64.RawURLEncoding
}

// ADR-008: Rollen und Nutzungsrecht aus Benutzernamen, AD-Gruppen und dem Inbetriebnahme-Schalter.
func TestCommRolesAndAccess(t *testing.T) {
	g := "cn=chat-mod,ou=gruppen,dc=x"
	c := CommunicationConfig{Roles: CommRoles{AdminUsers: []string{"chef"}, ModeratorGroups: []string{g}}, Access: CommAccess{Groups: []string{"cn=chat,dc=x"}}}
	no := false
	cases := []struct {
		p     Principal
		role  string
		allow bool
	}{
		{Principal{Username: "chef", Provider: "ldap"}, commRoleAdmin, true},
		{Principal{Username: "Chef", Provider: "ldap"}, commRoleAdmin, true},
		{Principal{Username: "m", Provider: "ldap", Groups: []string{g}}, commRoleMod, true},
		{Principal{Username: "u", Provider: "ldap", Groups: []string{"cn=chat,dc=x"}}, "", true},
		{Principal{Username: "x", Provider: "ldap"}, "", false},
		{Principal{Username: "pbadmin", Provider: "local", IsAdmin: true}, "", false}, // Telefonbuch-Admin ohne Zuordnung
		{Principal{Username: "chef", Provider: "ldap", Disabled: true}, commRoleAdmin, false},
	}
	for _, x := range cases {
		if r := c.roleFor(&x.p); r != x.role {
			t.Errorf("%s: Rolle %q, erwartet %q", x.p.Username, r, x.role)
		}
		if a := c.allowed(&x.p); a != x.allow {
			t.Errorf("%s: Zugang %v", x.p.Username, a)
		}
	}
	c.Roles.PhonebookAdminsAreUCNGAdmins = true
	if r := c.roleFor(&Principal{Username: "pbadmin", IsAdmin: true}); r != commRoleAdmin {
		t.Errorf("Inbetriebnahme: %q", r)
	}
	c.Access = CommAccess{AllowLocal: &no}
	if c.allowed(&Principal{Username: "lokal", Provider: "local"}) || !c.allowed(&Principal{Username: "l", Provider: "ldap"}) {
		t.Error("allowLocal=false")
	}
}
