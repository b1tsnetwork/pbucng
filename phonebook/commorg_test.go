package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// orgFixture: Dezernat (1) → Ordnungsamt (2) → Abteilung Gewerbe (3); Personen anna (Abteilung),
// bert (Amtsleitung fest eingetragen), carl (Leitungs-Schlagwort der Abteilungsebene), dora (inaktiv).
type orgFixture struct {
	dez, amt, abt          int64
	anna, bert, carl, dora int64
	leadTag                int64
}

func makeOrg(t *testing.T, a *App) orgFixture {
	t.Helper()
	ctx := context.Background()
	st := a.st()
	var f orgFixture
	var err error
	now := time.Now().UTC()
	emp := func(user, last string, active bool) int64 {
		id, err := st.CreateEmployee(ctx, &EmployeeRec{Username: user, LastName: last, FirstName: "X", Source: "manual", Active: active, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.anna, f.bert, f.carl, f.dora = emp("anna", "Albers", true), emp("bert", "Bauer", true), emp("carl", "Conrad", true), emp("dora", "Dorn", false)
	if f.dez, err = st.CreateOrgUnit(ctx, OrgUnit{Name: "Dezernat II", Short: "II"}); err != nil {
		t.Fatal(err)
	}
	if f.amt, err = st.CreateOrgUnit(ctx, OrgUnit{Name: "Ordnungsamt", Short: "32", Parent: f.dez, Leaders: []int64{f.bert}}); err != nil {
		t.Fatal(err)
	}
	if f.abt, err = st.CreateOrgUnit(ctx, OrgUnit{Name: "Gewerbe", Short: "32.1", Parent: f.amt}); err != nil {
		t.Fatal(err)
	}
	st.SetOrgUnit(ctx, []int64{f.anna, f.carl, f.dora}, f.abt)
	st.SetOrgUnit(ctx, []int64{f.bert}, f.amt)
	f.leadTag, err = st.CreateTag(ctx, "Sachgebietsleitung", "sonstiges", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEmployeeTags(ctx, f.carl, []TagInput{{"Sachgebietsleitung", "sonstiges"}}, "test", true); err != nil {
		t.Fatal(err)
	}
	lv, _ := json.Marshal([]OrgLevel{{Name: "Dezernat"}, {Name: "Amt"}, {Name: "Sachgebiet", LeaderTag: f.leadTag}})
	st.SetSetting(ctx, settingOrgLevels, string(lv))
	return f
}

func ids(xs ...int64) []string {
	var out []string
	for _, x := range xs {
		out = append(out, itoa64(x))
	}
	return out
}

func itoa64(x int64) string { return strconv.FormatInt(x, 10) }

func TestCommOrgGroups(t *testing.T) {
	a := testApp(t)
	f := makeOrg(t, a)
	ctx := context.Background()
	o, err := a.loadOrg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id int64) []string {
		e, _ := a.st().GetEmployee(ctx, id)
		return o.groupsFor(e)
	}
	g := func(kind string, id int64) string { return kind + ":" + itoa64(id) }
	want := map[int64][]string{
		f.anna: {g("org", f.abt), g("org", f.amt), g("org", f.dez)},
		f.bert: {g("lead", f.amt), g("org", f.amt), g("org", f.dez)},                  // Leitung fest eingetragen
		f.carl: {g("lead", f.abt), g("org", f.abt), g("org", f.amt), g("org", f.dez)}, // Leitungs-Schlagwort der Ebene
		f.dora: nil,                                                                   // inaktiv
	}
	for id, w := range want {
		got := get(id)
		if strings.Join(got, ",") != strings.Join(sortedCopy(w), ",") {
			t.Errorf("Person %d: %v, erwartet %v", id, got, sortedCopy(w))
		}
	}
	if p := o.path(f.abt); p != "II – Dezernat II › 32 – Ordnungsamt › 32.1 – Gewerbe" {
		t.Errorf("Pfad: %q", p)
	}
	// Zyklus im Baum hängt nicht
	o.units[f.dez] = OrgUnit{ID: f.dez, Name: "Dezernat II", Parent: f.abt}
	if c := o.chain(f.abt); len(c) != 3 {
		t.Errorf("Zyklus: %d", len(c))
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestCommOrgClaimsAndPush(t *testing.T) {
	e := ssoApp(t, func(c *CommunicationConfig) { c.AutoCreateOrgRooms, c.OrgRoomLevels = true, []int{2, 3} })
	f := makeOrg(t, e.a)
	ctx := context.Background()
	c, _ := e.login(t, "anna")
	code, ver := e.codeFor(t, c)
	st, cl := e.redeem(code, ver)
	if st != 200 || strings.Join(cl.Groups, ",") != strings.Join(sortedCopy([]string{"org:" + itoa64(f.abt), "org:" + itoa64(f.amt), "org:" + itoa64(f.dez)}), ",") {
		t.Fatalf("Gruppen im Einlösen: %d %v", st, cl.Groups)
	}
	// Organisationsstand: Kanäle nur für Ebene 2 und 3, Version stabil
	units, v1, err := e.a.orgPayload(ctx)
	if err != nil || len(units) != 3 {
		t.Fatal(err, units)
	}
	rooms := map[int64]bool{}
	for _, u := range units {
		rooms[u.ID] = u.Room
	}
	if rooms[f.dez] || !rooms[f.amt] || !rooms[f.abt] {
		t.Fatalf("Kanäle je Ebene: %v", rooms)
	}
	if _, v2, _ := e.a.orgPayload(ctx); v2 != v1 {
		t.Fatal("Version nicht stabil")
	}
	if err := e.a.pushOrg(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := e.a.pushOrg(ctx, false); err != nil { // unverändert: kein zweiter Aufruf
		t.Fatal(err)
	}
	e.sink.mu.Lock()
	n := len(e.sink.orgs)
	got := e.sink.orgs[0]
	e.sink.mu.Unlock()
	if n != 1 || got.Version != v1 || len(got.Units) != 3 {
		t.Fatalf("Abgleich: %d %+v", n, got)
	}
	// Umbenennung → neue Version, neuer Abgleich
	e.a.st().UpdateOrgUnit(ctx, OrgUnit{ID: f.amt, Name: "Bürger- und Ordnungsamt", Short: "32", Parent: f.dez, Leaders: []int64{f.bert}})
	if err := e.a.pushOrg(ctx, false); err != nil {
		t.Fatal(err)
	}
	e.sink.mu.Lock()
	n = len(e.sink.orgs)
	e.sink.mu.Unlock()
	if n != 2 {
		t.Fatalf("Umbenennung nicht abgeglichen: %d", n)
	}
	// falscher Schlüssel beim Empfänger → Fehler wird gemeldet
	e.sink.key = "falsch"
	if err := e.a.pushOrg(ctx, true); err == nil {
		t.Fatal("Fehler nicht erkannt")
	}
}

func TestCommPrincipalStatus(t *testing.T) {
	e := ssoApp(t, func(c *CommunicationConfig) { c.Access.Users = []string{"anna", "bert"} })
	ctx := context.Background()
	ids := map[string]string{}
	for _, u := range []string{"anna", "bert", "chef"} {
		e.login(t, u)
	}
	ps, _ := e.a.st().ListPrincipals(ctx)
	for _, p := range ps {
		ids[p.Username] = p.ID
	}
	// bert gesperrt (lokal), Nachprüfung fällig
	lu, _ := e.a.st().GetLocalUser(ctx, "bert")
	lu.Disabled = true
	e.a.st().UpsertLocalUser(ctx, *lu)
	p, _ := e.a.st().GetPrincipal(ctx, ids["bert"])
	p.CheckedAt = time.Now().Add(-time.Hour)
	e.a.st().UpsertPrincipal(ctx, p)
	body, _ := json.Marshal(map[string]any{"subjects": []string{ids["anna"], ids["bert"], ids["chef"], "p_gibtsnicht"}})
	if rec := e.internal("/api/internal/communication/principals", e.a.cfg.Communication.key(commKeyToUCNG), string(body)); rec.Code != 401 {
		t.Fatalf("falscher Schlüssel: %d", rec.Code)
	}
	rec := e.internal("/api/internal/communication/principals", e.a.cfg.Communication.key(commKeyFromUCNG), string(body))
	var out struct {
		Results []struct{ Subject, Status string }
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	got := map[string]string{}
	for _, r := range out.Results {
		got[r.Subject] = r.Status
	}
	if got[ids["anna"]] != "active" || got[ids["bert"]] != "disabled" || got[ids["chef"]] != "forbidden" || got["p_gibtsnicht"] != "unknown" {
		t.Fatalf("Status: %v (%s)", got, rec.Body)
	}
	// Sperre löst den Widerruf der Person aus
	found := false
	for _, g := range e.sink.wait(t, 1) {
		if len(g.Subjects) == 1 && g.Subjects[0] == ids["bert"] {
			found = true
		}
	}
	if !found {
		t.Fatal("kein Widerruf")
	}
}

func TestCommAdminPolicy(t *testing.T) {
	e := ssoApp(t, nil)
	admin := sessionCookie(t, e.a, "chef", true)
	user := sessionCookie(t, e.a, "anna", false)
	csrf := func(c *http.Cookie) string {
		var me map[string]any
		json.Unmarshal(getWith(e.h, "/api/me", c, "10.0.0.5").Body.Bytes(), &me)
		return me["csrf"].(string)
	}
	call := func(method string, c *http.Cookie, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/admin/communication", strings.NewReader(body))
		req.RemoteAddr = "10.0.0.5:1"
		req.AddCookie(c)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf(c))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("GET", user, ""); rec.Code != 403 {
		t.Fatalf("ohne Admin: %d", rec.Code)
	}
	rec := call("GET", admin, "")
	var st map[string]any
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st["source"] != "config" {
		t.Fatalf("GET: %d %v", rec.Code, st)
	}
	if rec := call("PUT", admin, `{"roles":{"adminGroups":["CN=X,DC=y"]}}`); rec.Code != 400 { // Gruppen ohne LDAP
		t.Fatalf("Gruppen ohne LDAP: %d %s", rec.Code, rec.Body)
	}
	if rec := call("PUT", admin, `{"orgRoomLevels":[0]}`); rec.Code != 400 {
		t.Fatalf("Ebene 0: %d", rec.Code)
	}
	rec = call("PUT", admin, `{"access":{"users":[" Anna ",""]},"roles":{"moderatorUsers":["BERT"]},"autoCreateOrgRooms":true,"orgRoomLevels":[3,2]}`)
	json.Unmarshal(rec.Body.Bytes(), &st)
	pol, _ := st["policy"].(map[string]any)
	if rec.Code != 200 || st["source"] != "admin" || pol == nil {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	cfg := e.a.commCfg()
	if strings.Join(cfg.Access.Users, ",") != "anna" || cfg.roleFor(&Principal{Username: "bert"}) != commRoleMod || !cfg.AutoCreateOrgRooms || len(cfg.OrgRoomLevels) != 2 || cfg.OrgRoomLevels[0] != 2 {
		t.Fatalf("wirksam: %+v", cfg.policy())
	}
	if e.a.cfg.Communication.AutoCreateOrgRooms || len(e.a.cfg.Communication.Access.Users) != 0 {
		t.Fatal("Konfiguration aus der Datei verändert")
	}
	// wirkt sofort: chef hat keinen Zugang mehr
	c, _ := e.login(t, "chef")
	_, ch := pkce()
	if _, u := e.authorize(t, c, randomToken(16), ch, ""); frag(u).Get("pb_error") != "forbidden" {
		t.Fatalf("Zugang nach Änderung: %v", u)
	}
	entries, _ := e.a.st().ListAudit(context.Background(), "", 50)
	logged := false
	for _, x := range entries {
		logged = logged || x.Action == "communication_policy"
	}
	if !logged {
		t.Fatal("nicht protokolliert")
	}
	rec = call("DELETE", admin, "")
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st["source"] != "config" || e.a.commCfg().AutoCreateOrgRooms {
		t.Fatalf("Zurücksetzen: %d %v", rec.Code, st["source"])
	}
	// ungültig gespeicherter Stand: Datei gilt
	e.a.st().SetSetting(context.Background(), settingCommPolicy, `{"roles":{"adminGroups":["cn=x"]}}`)
	e.a.commCfgInvalidate()
	if _, src := e.a.commCfgSource(); src != "config" {
		t.Fatalf("ungültiger Stand angewendet: %s", src)
	}
}
