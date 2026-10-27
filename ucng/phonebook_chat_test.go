// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// PH-05: Die Chat-Funktionen aus der Funktionsmatrix (Textchat, Personen, Spam-Bremsen) im
// Modus phonebook – mit Konten aus dem Telefonbuch statt reservierter Namen. Die Standalone-Tests
// bleiben unverändert; hier geht es darum, dass die Andockpunkte aus PH-02 bis PH-04 (Sitzungen
// mit Frist, Gruppen als Rollen, kurzlebige Bildadressen, 503 bei Ausfall …) nichts davon brechen.

// sseEvents liest Ereignisse aus dem Live-Stream (bis der Test endet).
func sseEvents(t *testing.T, url string) <-chan Event {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("Live-Stream: %d", res.StatusCode)
	}
	t.Cleanup(func() { res.Body.Close() })
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				var ev Event
				if json.Unmarshal([]byte(line[6:]), &ev) == nil {
					ch <- ev
				}
			}
		}
	}()
	return ch
}

func waitEvent(t *testing.T, ch <-chan Event, what string, ok func(Event) bool) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatalf("%s: Live-Stream beendet", what)
			}
			if ok(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("%s: kein passendes Ereignis", what)
		}
	}
}

func TestPhonebookChatParity(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	msgs := base + "/api/rooms/lobby/messages"
	anna, annaID := e.signIn(t, claimsFor("p_chat_anna", "anna", "Anna Berg", ""))
	ben, benID := e.signIn(t, claimsFor("p_chat_ben", "bweber", "Ben Weber", ""))
	sessOf := func(tok string) sessionInfo {
		return decode[sessionInfo](t, do(t, "GET", base+"/api/session", tok, ""))
	}

	t.Run("Echtzeit", func(t *testing.T) {
		ch := sseEvents(t, msgs[:len(msgs)-len("/messages")]+"/events?viewer="+sessOf(anna).Viewer)
		waitEvent(t, ch, "Verlauf", func(ev Event) bool { return ev.Type == "history" })
		m := decode[Message](t, expect(t, post(t, msgs, ben, `{"text":"Live aus dem Telefonbuch-Modus"}`), 201, "senden"))
		got := waitEvent(t, ch, "neue Nachricht", func(ev Event) bool { return ev.Message != nil && ev.Message.ID == m.ID })
		if got.Message.Name != "Ben Weber" || got.Message.UserID != benID {
			t.Fatalf("Absender: %+v", got.Message)
		}
		// ohne gültige Sitzung kein Live-Stream (Viewer ohne Sitzung)
		if c := status(t, "GET", base+"/api/rooms/lobby/events", "", ""); c != 401 {
			t.Fatalf("Live-Stream ohne Anmeldung: %d", c)
		}
	})

	t.Run("Verlauf", func(t *testing.T) {
		for i := 1; i <= 130; i++ {
			e.hub.Post("lobby", "Archiv", fmt.Sprintf("Archiv-Nachricht %d", i))
		}
		res := expect(t, do(t, "GET", msgs+"?limit=50", anna, ""), 200, "Seite 1")
		page := decode[[]Message](t, res)
		if len(page) != 50 || res.Header.Get("X-Has-More") != "true" {
			t.Fatalf("Seite 1: %d %s", len(page), res.Header.Get("X-Has-More"))
		}
		seen := map[int64]bool{}
		for _, m := range page {
			seen[m.ID] = true
		}
		for more := true; more; {
			res = do(t, "GET", fmt.Sprintf("%s?before=%d&limit=50", msgs, page[0].ID), anna, "")
			more = res.Header.Get("X-Has-More") == "true"
			page = decode[[]Message](t, res)
			for _, m := range page {
				if seen[m.ID] {
					t.Fatalf("doppelt: %d", m.ID)
				}
				seen[m.ID] = true
			}
		}
		if len(seen) < 131 {
			t.Fatalf("nicht alles geblättert: %d", len(seen))
		}
		if c := status(t, "GET", msgs, "", ""); c != 401 {
			t.Fatalf("Verlauf ohne Anmeldung: %d", c)
		}
	})

	t.Run("Antworten, Reaktionen, Bearbeiten, Löschen", func(t *testing.T) {
		q := decode[Message](t, expect(t, post(t, msgs, anna, `{"text":"Wer kommt mit in die Kantine?"}`), 201, "Frage"))
		ans := decode[Message](t, expect(t, post(t, msgs, ben, `{"text":"Ich!","replyTo":`+itoa64(q.ID)+`}`), 201, "Antwort"))
		if ans.Reply == nil || ans.Reply.Name != "Anna Berg" {
			t.Fatalf("Antwort-Vorschau: %+v", ans.Reply)
		}
		r := decode[Message](t, expect(t, do(t, "POST", msgs+"/"+itoa64(q.ID)+"/react", ben, `{"emoji":"👍"}`), 200, "Reaktion"))
		if len(r.Reactions) != 1 || r.Reactions[0].Users[0].Name != "Ben Weber" {
			t.Fatalf("Reaktion: %+v", r.Reactions)
		}
		expect(t, do(t, "PATCH", msgs+"/"+itoa64(q.ID), ben, `{"text":"gehackt"}`), 403, "fremde bearbeiten").Body.Close()
		ed := decode[Message](t, expect(t, do(t, "PATCH", msgs+"/"+itoa64(q.ID), anna, `{"text":"Wer kommt mit?"}`), 200, "bearbeiten"))
		if ed.EditedAt == nil || ed.Text != "Wer kommt mit?" {
			t.Fatalf("bearbeitet: %+v", ed)
		}
		expect(t, do(t, "DELETE", msgs+"/"+itoa64(ans.ID), anna, ""), 403, "fremde löschen").Body.Close()
		gone := decode[Message](t, expect(t, do(t, "DELETE", msgs+"/"+itoa64(ans.ID), ben, ""), 200, "selbst löschen"))
		if !gone.Deleted || gone.Text != "" {
			t.Fatalf("gelöscht: %+v", gone)
		}
	})

	t.Run("Threads und Umfragen", func(t *testing.T) {
		root := decode[Message](t, post(t, msgs, anna, `{"text":"Thema: Betriebsausflug"}`))
		expect(t, post(t, msgs, ben, `{"text":"Ich bringe Kuchen","thread":`+itoa64(root.ID)+`}`), 201, "Thread").Body.Close()
		th := jmap(t, expect(t, do(t, "GET", base+"/api/rooms/lobby/threads/"+itoa64(root.ID), anna, ""), 200, "Thread lesen"))
		if b, _ := json.Marshal(th); !strings.Contains(string(b), "Ich bringe Kuchen") {
			t.Fatalf("Thread: %s", b)
		}
		if c := status(t, "GET", base+"/api/rooms/lobby/threads/"+itoa64(root.ID), "", ""); c != 401 {
			t.Fatalf("Thread ohne Anmeldung: %d", c)
		}
		p := decode[Message](t, expect(t, post(t, msgs, anna, `{"text":"!umfrage Wann? | Montag | Dienstag"}`), 201, "Umfrage"))
		v := decode[Message](t, expect(t, post(t, msgs+"/"+itoa64(p.ID)+"/vote", ben, `{"option":1}`), 200, "abstimmen"))
		if v.Poll == nil || len(v.Poll.Options[1].Votes) != 1 || v.Poll.Options[1].Votes[0] != benID {
			t.Fatalf("Umfrage: %+v", v.Poll)
		}
	})

	t.Run("Suche, Erwähnungen, @Rolle, Ungelesen", func(t *testing.T) {
		adm := adminLogin(t, base)
		roles := decode[[]Role](t, expect(t, do(t, "PUT", base+"/api/admin/roles", adm, `{"roles":[{"name":"Einkauf","color":"#2e7d32","mentionable":true}]}`), 200, "Rolle"))
		expect(t, do(t, "POST", base+"/api/admin/users/"+benID+"/roles", adm, `{"roles":["`+roles[0].ID+`"]}`), 200, "Rolle vergeben").Body.Close()
		first := decode[Message](t, post(t, msgs, anna, `{"text":"Zählerstart"}`))
		post(t, msgs, anna, `{"text":"@Ben Weber hast du kurz Zeit?"}`).Body.Close()
		post(t, msgs, anna, `{"text":"@Einkauf bitte Papier bestellen"}`).Body.Close()
		post(t, msgs, anna, `{"text":"ohne Erwähnung"}`).Body.Close()
		got := decode[map[string]UnreadInfo](t, expect(t, do(t, "POST", base+"/api/unread", ben, `{"since":{"lobby":`+itoa64(first.ID)+`}}`), 200, "ungelesen"))
		if got["lobby"] != (UnreadInfo{Unread: 3, Mentions: 2}) {
			t.Fatalf("Ben: %+v", got)
		}
		// Rolle aus dem Admin-Bereich bleibt neben den Gruppen aus dem Telefonbuch erhalten
		e.srv.pbRevalidate(t.Context())
		if s := sessOf(ben); !strings.Contains(strings.Join(s.Roles, ","), roles[0].ID) {
			t.Fatalf("Rolle nach Nachprüfung verloren: %v", s.Roles)
		}
		hits := decode[[]SearchHit](t, expect(t, do(t, "GET", base+"/api/rooms/lobby/search?q=papier", anna, ""), 200, "Suche"))
		if len(hits) != 1 || hits[0].Message.Name != "Anna Berg" {
			t.Fatalf("Suche: %+v", hits)
		}
		if c := status(t, "GET", base+"/api/rooms/lobby/search?q=papier", "", ""); c != 401 {
			t.Fatalf("Suche ohne Anmeldung: %d", c)
		}
	})

	t.Run("Anpinnen und Melden", func(t *testing.T) {
		adm := adminLogin(t, base)
		m := decode[Message](t, post(t, msgs, anna, `{"text":"Bitte alle lesen: neue Durchwahl"}`))
		expect(t, post(t, msgs+"/"+itoa64(m.ID)+"/pin", ben, `{"pin":true}`), 403, "Pin ohne Recht").Body.Close()
		expect(t, do(t, "POST", base+"/api/admin/rooms/lobby/messages/"+itoa64(m.ID)+"/pin", adm, `{"pin":true}`), 200, "Pin Admin").Body.Close()
		pins := decode[[]Message](t, expect(t, do(t, "GET", base+"/api/admin/rooms/lobby/pins", adm, ""), 200, "Pins"))
		if len(pins) != 1 || pins[0].ID != m.ID {
			t.Fatalf("Pins: %+v", pins)
		}
		expect(t, post(t, msgs+"/"+itoa64(m.ID)+"/report", anna, `{"reason":"x"}`), 403, "eigene melden").Body.Close()
		expect(t, post(t, msgs+"/"+itoa64(m.ID)+"/report", ben, `{"reason":"falsche Nummer"}`), 201, "melden").Body.Close()
		if c := decode[map[string]int](t, do(t, "GET", base+"/api/admin/reports/count", adm, "")); c["open"] != 1 {
			t.Fatalf("Meldungen: %+v", c)
		}
	})

	t.Run("Präsenz", func(t *testing.T) {
		ch := sseEvents(t, base+"/api/rooms/lobby/events?viewer="+sessOf(ben).Viewer)
		waitEvent(t, ch, "Verlauf", func(ev Event) bool { return ev.Type == "history" })
		var p struct {
			Users []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"users"`
		}
		for i := 0; i < 50; i++ {
			json.NewDecoder(expect(t, do(t, "GET", base+"/api/rooms/lobby/presence", anna, ""), 200, "Präsenz").Body).Decode(&p)
			if strings.Contains(fmt.Sprint(p.Users), benID) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(fmt.Sprint(p.Users), benID) {
			t.Fatalf("Ben nicht online: %+v", p)
		}
		if c := status(t, "GET", base+"/api/rooms/lobby/presence", "", ""); c != 401 {
			t.Fatalf("Präsenz ohne Anmeldung: %d", c)
		}
	})

	t.Run("Direktnachrichten und Blockieren", func(t *testing.T) {
		dm := decode[DMInfo](t, expect(t, post(t, base+"/api/dms", anna, `{"userId":"`+benID+`"}`), 200, "DM öffnen"))
		dmURL := base + "/api/rooms/" + dm.Room + "/messages"
		ka, kb := testKeys(), testKeys()
		for _, x := range []struct {
			tok string
			k   KeyMaterial
		}{{anna, ka}, {ben, kb}} {
			expect(t, do(t, "POST", base+"/api/account/e2ee/setup", x.tok, jsonBody(map[string]any{"keys": x.k, "recovery": recMat()})), 200, "Schlüssel").Body.Close()
		}
		expect(t, post(t, dmURL, anna, jsonBody(map[string]string{"e2e": pbE2EMsg(ka.PubKey, kb.PubKey)})), 201, "verschlüsselte DM").Body.Close()
		if c := status(t, "POST", dmURL, anna, `{"text":"Klartext"}`); c == 201 {
			t.Fatal("Klartext-DM angenommen")
		}
		if l := myDMs(t, base, ben); len(l) != 1 || l[0].Room != dm.Room {
			t.Fatalf("Bens DMs: %+v", l)
		}
		if c := status(t, "POST", dmURL, ben, jsonBody(map[string]string{"text": "!umfrage A? | x | y"})); c == 201 {
			t.Fatal("Umfrage in verschlüsselter DM")
		}
		// Blockieren
		expect(t, post(t, base+"/api/blocks", anna, `{"userId":"`+benID+`","block":true}`), 200, "blockieren").Body.Close()
		expect(t, post(t, dmURL, ben, jsonBody(map[string]string{"e2e": pbE2EMsg(kb.PubKey, ka.PubKey)})), 403, "blockiert schreiben").Body.Close()
		expect(t, post(t, base+"/api/dms", ben, `{"userId":"`+annaID+`"}`), 403, "blockiert öffnen").Body.Close()
		expect(t, post(t, base+"/api/blocks", anna, `{"userId":"`+benID+`","block":false}`), 200, "freigeben").Body.Close()
		expect(t, post(t, dmURL, ben, jsonBody(map[string]string{"e2e": pbE2EMsg(kb.PubKey, ka.PubKey)})), 201, "wieder schreiben").Body.Close()
	})

	t.Run("Spam-Bremsen", func(t *testing.T) {
		adm := adminLogin(t, base)
		expect(t, do(t, "POST", base+"/api/admin/rooms", adm, `{"name":"ruhig","slowMode":30,"links":"known"}`), 201, "Kanal").Body.Close()
		url := base + "/api/rooms/ruhig/messages"
		expect(t, post(t, url, ben, `{"text":"eins"}`), 201, "erste").Body.Close()
		if res := post(t, url, ben, `{"text":"zwei"}`); res.StatusCode != 429 || !strings.Contains(errText(res), "Langsam-Modus") {
			t.Fatalf("Langsam-Modus: %d", res.StatusCode)
		}
		expect(t, do(t, "PATCH", base+"/api/admin/rooms/ruhig", adm, `{"description":"","slowMode":0}`), 200, "Langsam aus").Body.Close()
		// Konten aus dem Telefonbuch sind bekannte Personen: Links auch am ersten Tag
		expect(t, post(t, url, ben, `{"text":"Formular: https://intranet.example/formular"}`), 201, "Link (Konto aus dem Telefonbuch)").Body.Close()
		expect(t, post(t, msgs, ben, `{"text":"ok"}`), 201, "kurz 1.").Body.Close()
		expect(t, post(t, msgs, ben, `{"text":"OK"}`), 201, "kurz 2.").Body.Close()
		expect(t, post(t, msgs, ben, `{"text":"ok"}`), 429, "kurz 3.").Body.Close()
		expect(t, post(t, msgs, ben, `{"text":"Kauft Sachen bei uns, super billig!!"}`), 201, "1.").Body.Close()
		if res := post(t, msgs, ben, `{"text":"kauft sachen bei uns, super billig!!"}`); res.StatusCode != 429 {
			t.Fatalf("Wiederholung: %d", res.StatusCode)
		}
	})
}

// Standalone bleibt: neue Namen dürfen in „known“-Kanälen erst nach 24 Stunden Links posten.
func TestLinksKnownStandaloneUnchanged(t *testing.T) {
	ts, _ := testServerWith(t, nil)
	adm := adminLogin(t, ts.URL)
	expect(t, do(t, "POST", ts.URL+"/api/admin/rooms", adm, `{"name":"ruhig","links":"known"}`), 201, "Kanal").Body.Close()
	tok := claim(t, ts.URL, "Neu")
	if res := post(t, ts.URL+"/api/rooms/ruhig/messages", tok, `{"text":"https://example.com"}`); res.StatusCode != 403 {
		t.Fatalf("Link von neuem Namen: %d", res.StatusCode)
	}
}
