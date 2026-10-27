// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testUnits = []pbUnit{
	{ID: 1, Name: "Dezernat II", Short: "II", Path: "II – Dezernat II", Room: false},
	{ID: 2, Parent: 1, Name: "Ordnungsamt", Short: "32", Path: "II – Dezernat II › 32 – Ordnungsamt", Room: true},
	{ID: 3, Parent: 2, Name: "Gewerbe", Short: "32.1", Path: "… › 32.1 – Gewerbe", Room: true},
}

func (e *pbEnv) syncOrg(t *testing.T, version string, units []pbUnit) PBOrgResult {
	t.Helper()
	res := do(t, "PUT", e.ts.URL+"/api/integration/phonebook/org", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"version": version, "units": units}))
	var out PBOrgResult
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("Organisationsabgleich: %d", res.StatusCode)
	}
	return out
}

func roomByName(t *testing.T, e *pbEnv, name string) RoomInfo {
	t.Helper()
	for _, r := range e.hub.Rooms() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("Kanal %s fehlt", name)
	return RoomInfo{}
}

func pbRoomNames(t *testing.T, e *pbEnv, tok string) []string {
	t.Helper()
	var list []RoomInfo
	res := do(t, "GET", e.ts.URL+"/api/rooms", tok, "")
	json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	var out []string
	for _, r := range list {
		out = append(out, r.Name)
	}
	return out
}

func TestPhonebookOrgRooms(t *testing.T) {
	e := phonebookServer(t)
	if c := status(t, "PUT", e.ts.URL+"/api/integration/phonebook/org", "falsch", `{"units":[]}`); c != 401 {
		t.Fatalf("ohne Schlüssel: %d", c)
	}
	if c := status(t, "PUT", e.ts.URL+"/api/integration/phonebook/org", e.pb.cfg.key(pbKeyUCNGToPhonebook), `{"units":[]}`); c != 401 {
		t.Fatalf("Schlüssel der Gegenrichtung: %d", c)
	}
	res := e.syncOrg(t, "v1", testUnits)
	if strings.Join(res.Created, ",") != "32-ordnungsamt,32-1-gewerbe" {
		t.Fatalf("angelegt: %+v", res)
	}
	r := roomByName(t, e, "32-ordnungsamt")
	if strings.Join(r.ReadRoles, ",") != "pb:org:2" || strings.Join(r.ModRoles, ",") != "pb:lead:2" || r.Org != 2 || r.Group != pbOrgRoomsGrp || r.Description == "" {
		t.Fatalf("Kanal: %+v", r)
	}
	// unverändert: nichts zu tun
	if res := e.syncOrg(t, "v1", testUnits); len(res.Created)+len(res.Updated)+len(res.Archived) != 0 {
		t.Fatalf("zweiter Abgleich ändert: %+v", res)
	}
	// Umbenennung im Telefonbuch: automatisch vergebener Name zieht nach …
	u2 := append([]pbUnit(nil), testUnits...)
	u2[1].Name = "Bürgeramt"
	if res := e.syncOrg(t, "v2", u2); strings.Join(res.Updated, ",") != "32-buergeramt" {
		t.Fatalf("Umbenennung: %+v", res)
	}
	// … ein vom Admin vergebener Name bleibt
	if _, err := e.hub.UpdateRoomWith("32-1-gewerbe", RoomInput{Name: ptr("gewerbe-team"), ReadRoles: []string{"pb:org:3"}}); err != nil {
		t.Fatal(err)
	}
	u3 := append([]pbUnit(nil), u2...)
	u3[2].Name = "Gewerbeangelegenheiten"
	if res := e.syncOrg(t, "v3", u3); strings.Join(res.Updated, ",") != "gewerbe-team" {
		t.Fatalf("Admin-Name: %+v", res)
	}
	// Einheit fällt weg: Kanal bleibt, nur noch Team (nie plötzlich öffentlich)
	res = e.syncOrg(t, "v4", u3[:2])
	if strings.Join(res.Archived, ",") != "gewerbe-team" {
		t.Fatalf("archiviert: %+v", res)
	}
	r = roomByName(t, e, "gewerbe-team")
	if strings.Join(r.ReadRoles, ",") != RoleTeamOnly || !r.OrgArchived || len(r.ModRoles) != 0 {
		t.Fatalf("Archiv: %+v", r)
	}
	// unbekannte Telefonbuch-Gruppen lassen sich nicht vergeben
	info, err := e.hub.CreateRoomWith(RoomInput{Name: ptr("privat"), ReadRoles: []string{"pb:org:999", "pb:org:2"}})
	if err != nil || strings.Join(info.ReadRoles, ",") != "pb:org:2" {
		t.Fatalf("Filter: %+v %v", info, err)
	}
	// Katalog für den Admin-Bereich
	adm := jmap(t, do(t, "POST", e.ts.URL+"/api/admin/login", "", jsonBody(map[string]string{"password": testAdminPW})))["token"].(string)
	cat := jmap(t, do(t, "GET", e.ts.URL+"/api/admin/phonebook/groups", adm, ""))
	if cat["version"] != "v4" || len(cat["groups"].([]any)) != 4 {
		t.Fatalf("Katalog: %v", cat)
	}
	// ungültiger Stand wird abgelehnt
	bad := []pbUnit{{ID: 1, Name: ""}}
	if c := status(t, "PUT", e.ts.URL+"/api/integration/phonebook/org", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"units": bad})); c != 400 {
		t.Fatalf("ungültig: %d", c)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPhonebookOrgAccess(t *testing.T) {
	e := phonebookServer(t)
	e.syncOrg(t, "v1", testUnits)
	ca := claimsFor("p_anna", "anna", "Anna", "")
	ca.Groups = []string{"org:1", "org:2", "org:3"}
	cb := claimsFor("p_bert", "bert", "Bert", "")
	cb.Groups = []string{"lead:2", "org:1", "org:2"}
	cc := claimsFor("p_carl", "carl", "Carl", "")
	ta, _ := e.signIn(t, ca)
	tb, _ := e.signIn(t, cb)
	tc, _ := e.signIn(t, cc)
	has := func(list []string, n string) bool { return contains(list, n) }
	if s := jmap(t, do(t, "GET", e.ts.URL+"/api/session", ta, "")); s["canUpload"] != true {
		t.Fatalf("Konto aus dem Telefonbuch darf keine Bilder hochladen: %v", s["canUpload"])
	}
	if l := pbRoomNames(t, e, ta); !has(l, "32-ordnungsamt") || !has(l, "32-1-gewerbe") {
		t.Fatalf("anna sieht: %v", l)
	}
	if l := pbRoomNames(t, e, tb); !has(l, "32-ordnungsamt") || has(l, "32-1-gewerbe") {
		t.Fatalf("bert sieht: %v", l)
	}
	if l := pbRoomNames(t, e, tc); has(l, "32-ordnungsamt") {
		t.Fatalf("carl (ohne Einheit) sieht: %v", l)
	}
	if c := status(t, "GET", e.ts.URL+"/api/rooms/32-ordnungsamt/messages", tc, ""); c != 404 {
		t.Fatalf("fremder Verlauf: %d", c)
	}
	// Moderation im eigenen Kanal (Leitung), nicht anderswo
	var list []RoomInfo
	json.NewDecoder(do(t, "GET", e.ts.URL+"/api/rooms", tb, "").Body).Decode(&list)
	for _, r := range list {
		if r.Name == "32-ordnungsamt" && !r.Mod {
			t.Fatal("Leitung ohne Moderationskennzeichen")
		}
		if r.Name == "lobby" && r.Mod {
			t.Fatal("Moderation außerhalb des eigenen Kanals")
		}
	}
	msg := jmap(t, do(t, "POST", e.ts.URL+"/api/rooms/32-ordnungsamt/messages", ta, `{"text":"Hallo Amt"}`))
	id := int64(msg["id"].(float64))
	lob := jmap(t, do(t, "POST", e.ts.URL+"/api/rooms/lobby/messages", ta, `{"text":"Hallo alle"}`))
	lid := int64(lob["id"].(float64))
	if c := status(t, "DELETE", e.ts.URL+"/api/rooms/lobby/messages/"+itoa64(lid)+"/mod", tb, ""); c != 403 {
		t.Fatalf("Leitung löscht in der Lobby: %d", c)
	}
	if c := status(t, "DELETE", e.ts.URL+"/api/rooms/32-ordnungsamt/messages/"+itoa64(id)+"/mod", tb, ""); c != 200 {
		t.Fatalf("Leitung löscht im eigenen Kanal: %d", c)
	}
	// Gruppen entfallen (Wechsel der Einheit): offene Verbindung zum Kanal wird getrennt
	sess := jmap(t, do(t, "GET", e.ts.URL+"/api/session", ta, ""))
	stream := openStream(t, e.ts.URL, "32-1-gewerbe", sess["viewer"].(string))
	nc := ca
	nc.Groups = []string{"org:1"}
	e.pb.set(ca.Session, pbResult{Valid: true, Claims: &nc})
	if err := e.srv.pbRevalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream, "Einheit gewechselt")
	if l := pbRoomNames(t, e, ta); has(l, "32-1-gewerbe") {
		t.Fatalf("nach Wechsel noch sichtbar: %v", l)
	}
	// offene Verbindung zu einem weiter lesbaren Kanal bleibt bestehen (Sitzung erhalten)
	open2 := openStream(t, e.ts.URL, "lobby", sess["viewer"].(string))
	nc2 := nc
	nc2.Groups = []string{"org:1", "org:2"}
	e.pb.set(ca.Session, pbResult{Valid: true, Claims: &nc2})
	e.srv.pbRevalidate(context.Background())
	select {
	case <-open2:
		t.Fatal("Lobby-Stream nach Gruppenänderung getrennt")
	case <-time.After(300 * time.Millisecond):
	}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

// ADR-010: private Bilder nur kurz und nur mit Sitzung und Leserecht.
func TestPhonebookPrivateMedia(t *testing.T) {
	e := phonebookServer(t)
	e.syncOrg(t, "v1", testUnits)
	ca := claimsFor("p_anna", "anna", "Anna", "")
	ca.Groups = []string{"org:2"}
	ta, _ := e.signIn(t, ca)
	tc, _ := e.signIn(t, claimsFor("p_carl", "carl", "Carl", ""))
	dir := filepath.Join(e.hub.Media().Dir(), "uploads")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "p_test0123456789.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644)
	src := e.hub.privateMediaSrc("/media/uploads/p_test0123456789.png", "32-ordnungsamt", false)
	if !strings.Contains(src, "&r=32-ordnungsamt") {
		t.Fatalf("Adresse: %s", src)
	}
	viewer := func(tok string) string {
		return jmap(t, do(t, "GET", e.ts.URL+"/api/session", tok, ""))["viewer"].(string)
	}
	get := func(u string) int { return status(t, "GET", e.ts.URL+u, "", "") }
	if c := get(src); c != 404 {
		t.Fatalf("ohne Sitzung: %d", c)
	}
	if c := get(src + "&v=" + viewer(ta)); c != 200 {
		t.Fatalf("mit Sitzung: %d", c)
	}
	if c := get(src + "&v=" + viewer(tc)); c != 404 {
		t.Fatalf("Sitzung ohne Leserecht: %d", c)
	}
	if c := get(strings.Replace(src, "&r=32-ordnungsamt", "&r=lobby", 1) + "&v=" + viewer(tc)); c != 404 {
		t.Fatalf("Kanal ausgetauscht: %d", c)
	}
	// abgelaufen bzw. zu lange gültig
	exp := time.Now().Add(-time.Minute).Unix()
	old := "/media/uploads/p_test0123456789.png?exp=" + jsonNum(exp) + "&r=32-ordnungsamt&sig=" + e.hub.mod.MediaSig(mediaSigSubject("p_test0123456789.png", "32-ordnungsamt", false), exp)
	if c := get(old + "&v=" + viewer(ta)); c != 404 {
		t.Fatalf("abgelaufen: %d", c)
	}
	far := time.Now().Add(2 * 24 * time.Hour).Unix()
	long := "/media/uploads/p_test0123456789.png?exp=" + jsonNum(far) + "&r=32-ordnungsamt&sig=" + e.hub.mod.MediaSig(mediaSigSubject("p_test0123456789.png", "32-ordnungsamt", false), far)
	if c := get(long + "&v=" + viewer(ta)); c != 404 {
		t.Fatalf("lange Gültigkeit angenommen: %d", c)
	}
	// Admin-Adresse ohne Sitzung (15 min), aber nicht mit der Signatur einer Chat-Adresse
	adm := e.hub.privateMediaSrc("/media/uploads/p_test0123456789.png", "32-ordnungsamt", true)
	if c := get(adm); c != 200 {
		t.Fatalf("Admin-Adresse: %d", c)
	}
	if c := get(src + "&a=1"); c != 404 {
		t.Fatalf("Chat-Signatur als Admin-Adresse: %d", c)
	}
	// Widerruf der Sitzung: sofort kein Abruf mehr
	v := viewer(ta)
	do(t, "POST", e.ts.URL+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"sessions": []string{ca.Session}})).Body.Close()
	if c := get(src + "&v=" + v); c != 404 {
		t.Fatalf("nach Widerruf: %d", c)
	}
	// Neu signieren: nur für Bilder, die im Kanal vorkommen, und nur mit Leserecht
	ta2, _ := e.signIn(t, ca)
	e.hub.mu.Lock()
	r := e.hub.rooms["32-ordnungsamt"]
	r.history = append(r.history, Message{ID: 9999, Room: r.name, Media: &Media{Kind: "image", Src: "/media/uploads/p_test0123456789.png"}})
	e.hub.mu.Unlock()
	re := jmap(t, do(t, "GET", e.ts.URL+"/api/rooms/32-ordnungsamt/media?src=/media/uploads/p_test0123456789.png", ta2, ""))
	if s, _ := re["src"].(string); !strings.Contains(s, "&sig=") {
		t.Fatalf("neu signiert: %v", re)
	}
	if c := status(t, "GET", e.ts.URL+"/api/rooms/32-ordnungsamt/media?src=/media/uploads/p_anderes.png", ta2, ""); c != 404 {
		t.Fatalf("fremdes Bild neu signiert: %d", c)
	}
	if c := status(t, "GET", e.ts.URL+"/api/rooms/32-ordnungsamt/media?src=/media/uploads/p_test0123456789.png", tc, ""); c != 404 {
		t.Fatalf("ohne Leserecht neu signiert: %d", c)
	}
	// außerhalb des Modus phonebook: unverändert 1–2 Tage, ohne Sitzung
	h := hostedServer(t)
	if s := h.hub.privateMediaSrc("/media/uploads/p_x.png", "lobby", false); strings.Contains(s, "&r=") {
		t.Fatalf("hosted verändert: %s", s)
	}
	_ = http.StatusOK
}

// Austritt: sperren, bei Rückkehr wieder frei, nach Frist anonymisieren.
func TestPhonebookLifecycle(t *testing.T) {
	e := phonebookServer(t)
	ctx := context.Background()
	ca := claimsFor("p_anna", "anna", "Anna Albers", "")
	ta, ida := e.signIn(t, ca)
	cb := claimsFor("p_bert", "bert", "Bert Bauer", "moderator")
	tb, _ := e.signIn(t, cb)
	jmap(t, do(t, "POST", e.ts.URL+"/api/rooms/lobby/messages", ta, `{"text":"Meine letzte Nachricht"}`))
	if c := status(t, "POST", e.ts.URL+"/api/account/e2ee/setup", ta, jsonBody(map[string]any{"keys": testKeys(), "recovery": recMat()})); c != 200 {
		t.Fatalf("E2EE: %d", c)
	}
	// Status-Abgleich: anna ausgeschieden, bert aktiv
	e.pb.status["p_anna"] = "disabled"
	if err := e.srv.pbLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	if c := status(t, "GET", e.ts.URL+"/api/session", ta, ""); c != 401 {
		t.Fatalf("Sitzung nach Austritt: %d", c)
	}
	if c := status(t, "GET", e.ts.URL+"/api/session", tb, ""); c != 200 {
		t.Fatalf("andere Person betroffen: %d", c)
	}
	// Rückkehr innerhalb der Frist: gleiches Konto
	e.pb.status["p_anna"] = "active"
	e.srv.pbLifecycle(ctx)
	_, again := e.signIn(t, ca)
	if again != ida {
		t.Fatal("Rückkehr: neues Konto")
	}
	// erneut ausgeschieden, Frist läuft ab
	e.pb.status["p_anna"] = "unknown"
	e.srv.pbLifecycle(ctx)
	e.clock.add(89 * 24 * time.Hour)
	e.srv.pbLifecycle(ctx)
	if info, _ := e.users.Get(ida); strings.HasPrefix(info.Name, pbFormerName) {
		t.Fatal("vor Ablauf der Frist anonymisiert")
	}
	e.clock.add(2 * 24 * time.Hour)
	if err := e.srv.pbLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	info, _ := e.users.Get(ida)
	if !strings.HasPrefix(info.Name, pbFormerName) || info.Account == nil || !info.Account.Disabled || info.Account.HasKeys {
		t.Fatalf("anonymisiert: %+v %+v", info, info.Account)
	}
	for _, m := range hist(e.hub, "lobby") {
		if m.UserID == ida && m.Name != info.Name {
			t.Fatalf("Nachricht trägt alten Namen: %q", m.Name)
		}
	}
	// Rückkehr nach der Anonymisierung: neues Konto, nichts vom alten
	_, fresh := e.signIn(t, ca)
	if fresh == ida {
		t.Fatal("anonymisiertes Konto wieder verknüpft")
	}
	// Telefonbuch nicht erreichbar: nichts sperren
	e.pb.setDown(true)
	if err := e.srv.pbLifecycle(ctx); err == nil {
		t.Fatal("Ausfall nicht erkannt")
	}
	e.pb.setDown(false)
	// Sperre per Widerruf (AD) markiert ebenfalls den Austritt
	do(t, "POST", e.ts.URL+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"subjects": []string{"p_bert"}})).Body.Close()
	e.users.mu.Lock()
	gone := e.users.byExt[extKey(pbProvider, "p_bert")].Member.Ext.Gone
	e.users.mu.Unlock()
	if gone.IsZero() {
		t.Fatal("Widerruf der Person ohne Austrittsmarke")
	}
}
