// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"testing"
	"time"
)

// PH-05: Direktnachricht aus der Visitenkarte, Zähler „neu seit letztem Besuch“.

func TestPhonebookDMLink(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	anna, _ := e.signIn(t, claimsFor("p_link_anna", "anna", "Anna Berg", ""))
	ben, benID := e.signIn(t, claimsFor("p_link_ben", "bweber", "Ben Weber", ""))
	url := base + "/api/integration/phonebook/user?subject="

	got := jmap(t, expect(t, do(t, "GET", url+"p_link_ben", anna, ""), 200, "auflösen"))
	if got["id"] != benID || got["name"] != "Ben Weber" || got["e2e"] != false {
		t.Fatalf("Ben: %v", got)
	}
	expect(t, do(t, "POST", base+"/api/account/e2ee/setup", ben, jsonBody(map[string]any{"keys": testKeys(), "recovery": recMat()})), 200, "Schlüssel").Body.Close()
	if got = jmap(t, do(t, "GET", url+"p_link_ben", anna, "")); got["e2e"] != true {
		t.Fatalf("Schlüssel nicht erkannt: %v", got)
	}
	for _, c := range []struct {
		tok, subject string
		want         int
		what         string
	}{
		{"", "p_link_ben", 401, "ohne Anmeldung"},
		{anna, "p_link_anna", 400, "sich selbst"},
		{anna, "p_gibtsnicht", 404, "noch nie angemeldet"},
		{anna, "ben", 400, "kein Principal"},
	} {
		res := do(t, "GET", url+c.subject, c.tok, "")
		if out := jmap(t, res); res.StatusCode != c.want {
			t.Fatalf("%s: %d %v", c.what, res.StatusCode, out)
		} else if c.want == 404 && out["error"] != ErrPBNotYet.Error() {
			t.Fatalf("Hinweis: %v", out)
		}
	}
	// ausgeschieden: nicht mehr erreichbar
	e.users.ExtStatus([]pbStatus{{Subject: "p_link_ben", Status: "disabled"}})
	if c := status(t, "GET", url+"p_link_ben", anna, ""); c != 404 {
		t.Fatalf("nach Austritt: %d", c)
	}
}

func TestPhonebookNews(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	key := e.pb.cfg.key(pbKeyPhonebookToUCNG)
	news := func(subject string) NewsInfo {
		t.Helper()
		return decode[NewsInfo](t, expect(t, do(t, "POST", base+"/api/integration/phonebook/news", key, jsonBody(map[string]string{"subject": subject})), 200, "news"))
	}
	anna, annaID := e.signIn(t, claimsFor("p_news_anna", "anna", "Anna Berg", ""))
	ben, benID := e.signIn(t, claimsFor("p_news_ben", "bweber", "Ben Weber", ""))
	carl, _ := e.signIn(t, claimsFor("p_news_carl", "cklein", "Carl Klein", ""))

	// nur das Telefonbuch darf fragen
	for _, tok := range []string{"", anna, e.pb.cfg.key(pbKeyUCNGToPhonebook)} {
		if c := status(t, "POST", base+"/api/integration/phonebook/news", tok, `{"subject":"p_news_ben"}`); c != http.StatusUnauthorized {
			t.Fatalf("news mit falschem Schlüssel: %d", c)
		}
	}
	if n := news("p_news_ben"); n.DMs != 0 || n.Mentions != 0 {
		t.Fatalf("am Anfang: %+v", n)
	}

	// Direktnachricht (verschlüsselt), Erwähnung, @Rolle, gewöhnliche Nachricht, eigene, blockierte
	ka, kb := testKeys(), testKeys()
	for _, x := range []struct {
		tok string
		k   KeyMaterial
	}{{anna, ka}, {ben, kb}} {
		expect(t, do(t, "POST", base+"/api/account/e2ee/setup", x.tok, jsonBody(map[string]any{"keys": x.k, "recovery": recMat()})), 200, "Schlüssel").Body.Close()
	}
	dm := decode[DMInfo](t, post(t, base+"/api/dms", anna, `{"userId":"`+benID+`"}`))
	expect(t, post(t, base+"/api/rooms/"+dm.Room+"/messages", anna, jsonBody(map[string]string{"e2e": pbE2EMsg(ka.PubKey, kb.PubKey)})), 201, "DM").Body.Close()
	msgs := base + "/api/rooms/lobby/messages"
	adm := adminLogin(t, base)
	roles := decode[[]Role](t, expect(t, do(t, "PUT", base+"/api/admin/roles", adm, `{"roles":[{"name":"Poststelle","color":"#2e7d32","mentionable":true},{"name":"Geheim","color":"#c2185b"}]}`), 200, "Rollen"))
	expect(t, do(t, "POST", base+"/api/admin/users/"+benID+"/roles", adm, `{"roles":["`+roles[0].ID+`"]}`), 200, "Rolle").Body.Close()
	expect(t, post(t, msgs, anna, `{"text":"@Ben Weber kurze Frage"}`), 201, "Erwähnung").Body.Close()
	expect(t, post(t, msgs, anna, `{"text":"@Poststelle Paket ist da"}`), 201, "@Rolle").Body.Close()
	expect(t, post(t, msgs, anna, `{"text":"Guten Morgen zusammen"}`), 201, "gewöhnlich").Body.Close()
	expect(t, post(t, msgs, ben, `{"text":"@Ben Weber ich selbst"}`), 201, "eigene").Body.Close()
	expect(t, post(t, base+"/api/blocks", ben, `{"userId":"`+sessOfID(t, base, carl)+`","block":true}`), 200, "blockieren").Body.Close()
	expect(t, post(t, msgs, carl, `{"text":"@Ben Weber hallo"}`), 201, "blockierte Person").Body.Close()
	// Kanal, den Ben nicht lesen darf
	expect(t, do(t, "POST", base+"/api/admin/rooms", adm, `{"name":"geheim","readRoles":["`+roles[1].ID+`"]}`), 201, "privater Kanal").Body.Close()
	expect(t, do(t, "POST", base+"/api/admin/users/"+annaID+"/roles", adm, `{"roles":["`+roles[1].ID+`"]}`), 200, "Rolle Anna").Body.Close()
	expect(t, post(t, base+"/api/rooms/geheim/messages", anna, `{"text":"@Ben Weber streng geheim"}`), 201, "privater Kanal schreiben").Body.Close()
	// gelöscht (Erwähnung und Direktnachricht)
	m := decode[Message](t, post(t, msgs, anna, `{"text":"@Ben Weber vertippt"}`))
	expect(t, do(t, "DELETE", msgs+"/"+itoa64(m.ID), anna, ""), 200, "löschen").Body.Close()
	m = decode[Message](t, expect(t, post(t, base+"/api/rooms/"+dm.Room+"/messages", anna, jsonBody(map[string]string{"e2e": pbE2EMsg(ka.PubKey, kb.PubKey)})), 201, "DM 2"))
	expect(t, do(t, "DELETE", base+"/api/rooms/"+dm.Room+"/messages/"+itoa64(m.ID), anna, ""), 200, "DM löschen").Body.Close()

	if n := news("p_news_ben"); n.DMs != 1 || n.Mentions != 2 {
		t.Fatalf("Ben: %+v", n)
	}
	if n := news("p_news_anna"); n.DMs != 0 || n.Mentions != 0 {
		t.Fatalf("Anna: %+v", n)
	}
	// Ben war im Chat: danach ist nichts mehr neu (Gelesen-Stand bleibt im Browser)
	e.clock.add(2 * time.Minute)
	status(t, "GET", base+"/api/rooms", ben, "")
	if n := news("p_news_ben"); n.DMs != 0 || n.Mentions != 0 {
		t.Fatalf("nach Besuch: %+v", n)
	}
	// unbekannt oder ausgeschieden: nichts
	if n := news("p_gibtsnicht"); n.DMs != 0 || n.Mentions != 0 || !n.Since.IsZero() {
		t.Fatalf("unbekannt: %+v", n)
	}
	e.users.ExtStatus([]pbStatus{{Subject: "p_news_anna", Status: "disabled"}})
	if n := news("p_news_anna"); !n.Since.IsZero() {
		t.Fatalf("ausgeschieden: %+v", n)
	}
}

func sessOfID(t *testing.T, base, tok string) string {
	t.Helper()
	return decode[sessionInfo](t, do(t, "GET", base+"/api/session", tok, "")).ID
}
