// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ucng/internal/voicerelay"
)

// PH-06 bis PH-10: die restlichen Zeilen der Funktionsmatrix im Modus phonebook – Medien,
// Sprachkanäle (eingebaut und abgesetzte Sprach-Server), Konferenz-Werkzeuge, temporäre Kanäle,
// Bots, Moderation, Automoderation, Datenschutz, Vorlagen, Statistik, Ankündigungen und die
// Betriebssperren für Beacon, externe GIFs und das Widget (ADR-009).

func TestPhonebookMediaAndLocks(t *testing.T) {
	var beaconHits atomic.Int32
	beacon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { beaconHits.Add(1); w.Write([]byte("{}")) }))
	defer beacon.Close()
	e := phonebookServer(t)
	e.hub.Media().SetDir(t.TempDir())
	base := e.ts.URL
	adm := adminLogin(t, base)
	tok, _ := e.signIn(t, claimsFor("p_med_anna", "anna", "Anna Berg", ""))

	t.Run("Emotes und GIF-Bibliothek", func(t *testing.T) {
		expect(t, upload(t, base+"/api/admin/emotes", adm, map[string]string{"name": "daumen"}, "d.png", testPNG(64, 64, color.RGBA{1, 200, 1, 255})), 201, "Emote").Body.Close()
		g := decode[GIFInfo](t, expect(t, upload(t, base+"/api/admin/gifs", adm, map[string]string{"title": "Applaus"}, "a.gif", testGIF(200, 120, 3)), 201, "GIF"))
		m := decode[Message](t, expect(t, post(t, base+"/api/rooms/lobby/messages", tok, `{"text":"super :daumen:"}`), 201, "mit Emote"))
		if !strings.Contains(m.Text, ":daumen:") {
			t.Fatalf("Emote: %+v", m)
		}
		gm := decode[Message](t, expect(t, post(t, base+"/api/rooms/lobby/messages", tok, `{"gif":"lib:`+g.ID+`"}`), 201, "GIF senden"))
		if gm.Media == nil || gm.Media.Src != g.URL {
			t.Fatalf("GIF: %+v", gm.Media)
		}
	})

	t.Run("Betriebssperren Beacon und externe GIFs (ADR-009)", func(t *testing.T) {
		var pub struct {
			Phonebook *PublicPhonebook `json:"phonebook"`
		}
		json.NewDecoder(do(t, "GET", base+"/api/settings", "", "").Body).Decode(&pub)
		if l := strings.Join(pub.Phonebook.Locks, ","); !strings.Contains(l, "beacon") || !strings.Contains(l, "externalGifs") || !strings.Contains(l, "guests") {
			t.Fatalf("Sperren nicht überprüfbar: %q", l)
		}
		if pub.Phonebook.TranscriptKeepDays != pbDefaultTranscriptDays || pub.Phonebook.STT {
			t.Fatalf("Transkripte (ADR-011): %+v", pub.Phonebook)
		}
		if c := status(t, "PUT", base+"/api/admin/media/settings", adm, `{"emoteSize":32,"gifsEnabled":true,"gifWidth":300,"provider":"giphy","apiKey":"abc"}`); c != http.StatusForbidden {
			t.Fatalf("GIPHY einrichten: %d", c)
		}
		expect(t, do(t, "PUT", base+"/api/admin/media/settings", adm, `{"emoteSize":32,"gifsEnabled":true,"gifWidth":300}`), 200, "ohne Anbieter").Body.Close()
		// auch ein früher gespeicherter Schlüssel wird nicht benutzt
		e.hub.Media().mu.Lock()
		e.hub.Media().cfg.Provider, e.hub.Media().cfg.APIKey = "giphy", "alt"
		e.hub.Media().mu.Unlock()
		if res := do(t, "GET", base+"/api/gifs/web?q=katze", tok, ""); res.StatusCode == 200 {
			t.Fatal("Web-Suche trotz Sperre")
		}
		if pm := decode[PublicMedia](t, do(t, "GET", base+"/api/media", "", "")); pm.Web != "" || pm.Beacon {
			t.Fatalf("Oberfläche bietet externe Quellen an: %+v", pm)
		}
		if c := status(t, "POST", base+"/api/rooms/lobby/messages", tok, `{"gif":"giphy:123"}`); c == 201 {
			t.Fatal("GIPHY-GIF trotz Sperre gesendet")
		}
		for _, x := range []struct{ m, p, body string }{
			{"PUT", "/api/admin/beacon", `{"url":"` + beacon.URL + `","listed":true}`},
			{"POST", "/api/admin/beacon/sync", ""},
			{"GET", "/api/admin/beacon/collections?url=" + beacon.URL, ""},
			{"GET", "/api/admin/i18n/beacon?url=" + beacon.URL, ""},
			{"POST", "/api/admin/i18n/beacon/install", `{"url":"` + beacon.URL + `","code":"fr"}`},
		} {
			if c := status(t, x.m, base+x.p, adm, x.body); c != http.StatusForbidden {
				t.Fatalf("%s %s: %d", x.m, x.p, c)
			}
		}
		if c := status(t, "GET", base+"/api/gifs/beacon?q=x", tok, ""); c == 200 {
			t.Fatal("Beacon-GIFs trotz Sperre")
		}
		// gespeicherte Beacon-Einstellung (z. B. aus der Zeit vor der Sperre) wird nicht benutzt
		e.hub.Beacon().Set(BeaconSettings{URL: beacon.URL, Listed: true})
		e.srv.beaconSync(t.Context())
		if n := beaconHits.Load(); n != 0 {
			t.Fatalf("Beacon trotz Sperre kontaktiert: %d", n)
		}
	})

	t.Run("kein Widget, keine fremden Herkünfte (RC)", func(t *testing.T) {
		if c := status(t, "POST", base+"/api/admin/widgets", adm, `{"room":"lobby","domains":["intranet.example"]}`); c != http.StatusForbidden {
			t.Fatalf("Widget anlegen: %d", c)
		}
		req, _ := http.NewRequest("GET", base+"/api/rooms", nil)
		req.Header.Set("Origin", "https://irgendwo.example")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("CORS für fremde Seite: %q", got)
		}
		// Widget-Skript selbst bleibt abrufbar (statisch, ohne Daten)
		if c := status(t, "GET", base+"/embed/chat.js", "", ""); c != 200 {
			t.Fatalf("Widget-Skript: %d", c)
		}
	})
}

// Standalone: Beacon und Widget bleiben unverändert verfügbar.
func TestLocksStandaloneUnchanged(t *testing.T) {
	ts, srv := testServerWith(t, nil)
	adm := adminLogin(t, ts.URL)
	if c := status(t, "POST", ts.URL+"/api/admin/widgets", adm, `{"room":"lobby"}`); c == http.StatusForbidden {
		t.Fatal("Widget im Standalone-Betrieb gesperrt")
	}
	if nw, nb := srv.hub.Media().locks(); nw || nb {
		t.Fatal("Sperren ohne Modus phonebook")
	}
	if c := status(t, "PUT", ts.URL+"/api/admin/media/settings", adm, `{"emoteSize":32,"gifsEnabled":true,"gifWidth":300,"provider":"giphy","apiKey":"abc"}`); c != 200 {
		t.Fatalf("GIPHY im Standalone-Betrieb: %d", c)
	}
}

func TestPhonebookVoice(t *testing.T) {
	e := phonebookServerWith(t, func(c *Config) { c.MaxRooms = 30 })
	base := e.ts.URL
	adm := adminLogin(t, base)
	anna, annaID := e.signIn(t, claimsFor("p_v_anna", "anna", "Anna Berg", ""))
	ben, benID := e.signIn(t, claimsFor("p_v_ben", "bweber", "Ben Weber", ""))
	name := "talk"
	if _, err := e.hub.CreateRoomWith(RoomInput{Name: &name, Voice: &VoiceConfig{}}); err != nil {
		t.Fatal(err)
	}

	t.Run("Sprachkanal mit Konten aus dem Telefonbuch", func(t *testing.T) {
		ja, code := voiceJoin(t, base, anna, "talk")
		if code != 200 || !ja.Speak || ja.Guest {
			t.Fatalf("Beitritt: %d %+v", code, ja)
		}
		ca, _ := voiceDial(t, base, ja)
		jb, _ := voiceJoin(t, base, ben, "talk")
		cb, _ := voiceDial(t, base, jb)
		waitPeers(t, e.srv.voice, "talk", 2)
		ca.WriteBinary([]byte{1, 0, 1, 200, 0xAA, 0xBB})
		for {
			op, p, err := cb.Read(2 * time.Second)
			if err != nil {
				t.Fatalf("Audio: %v", err)
			}
			if op == 2 && len(p) >= 8 {
				break
			}
		}
		if _, code := voiceJoin(t, base, "", "talk"); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("ohne Anmeldung: %d", code)
		}
	})

	t.Run("Verschieben durch Team (im Admin-Bereich vergeben)", func(t *testing.T) {
		b := "besprechung"
		e.hub.CreateRoomWith(RoomInput{Name: &b, Voice: &VoiceConfig{}, ReadRoles: []string{RoleTeamOnly}})
		if res := post(t, base+"/api/voice/talk/move", ben, `{"userId":"`+annaID+`","to":"besprechung"}`); res.StatusCode != http.StatusForbidden {
			t.Fatalf("ohne Recht verschoben: %d", res.StatusCode)
		}
		ja, _ := voiceJoin(t, base, anna, "talk")
		voiceDial(t, base, ja)
		waitPeers(t, e.srv.voice, "talk", 1)
		expect(t, do(t, "POST", base+"/api/admin/users/"+benID+"/team", adm, `{"team":true}`), 200, "Team").Body.Close()
		e.srv.pbRevalidate(t.Context()) // Nachprüfung lässt das Team-Abzeichen stehen
		if res := post(t, base+"/api/voice/talk/move", ben, `{"userId":"`+annaID+`","to":"besprechung"}`); res.StatusCode != 200 {
			t.Fatalf("verschieben: %d %s", res.StatusCode, errText(res))
		}
	})

	t.Run("Bühne, Freigabe-Rechte, Gäste-Regel", func(t *testing.T) {
		st := "podium"
		if _, err := e.hub.CreateRoomWith(RoomInput{Name: &st, Voice: &VoiceConfig{Stage: true, Guests: "speak", Screen: "roles", Video: "speakers"}}); err != nil {
			t.Fatal(err)
		}
		join := func(tok string) (map[string]any, int) {
			res := post(t, base+"/api/voice/podium/join", tok, `{}`)
			defer res.Body.Close()
			var m map[string]any
			json.NewDecoder(res.Body).Decode(&m)
			return m, res.StatusCode
		}
		a, code := join(anna)
		if code != 200 || a["speak"] != false || a["guest"] != false || a["moderate"] != false || a["screen"] != false || a["video"] != false {
			t.Fatalf("Publikum auf der Bühne: %d %v", code, a)
		}
		b, _ := join(ben) // Team (oben vergeben)
		if b["speak"] != true || b["moderate"] != true || b["screen"] != true || b["video"] != true {
			t.Fatalf("Team auf der Bühne: %v", b)
		}
		// Gäste-Regel „sprechen“ öffnet im Modus phonebook nichts: ohne Konto kein Beitritt
		if _, code := join(""); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("Gast trotz Gäste-Regel: %d", code)
		}
	})

	t.Run("Breakout-Räume", func(t *testing.T) {
		st := "buehne"
		e.hub.CreateRoomWith(RoomInput{Name: &st, Voice: &VoiceConfig{Stage: true}})
		jb, _ := voiceJoin(t, base, ben, "buehne")
		voiceDial(t, base, jb)
		for i := 0; i < 3; i++ {
			tok, _ := e.signIn(t, claimsFor(fmt.Sprintf("p_v_g%d", i), fmt.Sprintf("gast%d", i), fmt.Sprintf("Gruppe Person %d", i), ""))
			j, _ := voiceJoin(t, base, tok, "buehne")
			voiceDial(t, base, j)
		}
		waitPeers(t, e.srv.voice, "buehne", 4)
		if res := post(t, base+"/api/voice/buehne/breakout", anna, `{"groups":2}`); res.StatusCode != http.StatusForbidden {
			t.Fatalf("Breakout ohne Recht: %d", res.StatusCode)
		}
		res := post(t, base+"/api/voice/buehne/breakout", ben, `{"groups":2,"minutes":1}`)
		var b BreakoutInfo
		json.NewDecoder(res.Body).Decode(&b)
		res.Body.Close()
		if res.StatusCode != 200 || len(b.Rooms) != 2 {
			t.Fatalf("Breakout: %d %+v", res.StatusCode, b)
		}
	})

	t.Run("Abmeldung im Telefonbuch trennt den Sprachkanal", func(t *testing.T) {
		c := claimsFor("p_v_dora", "dkrause", "Dora Krause", "")
		tok, id := e.signIn(t, c)
		j, _ := voiceJoin(t, base, tok, "talk")
		voiceDial(t, base, j)
		for i := 0; i < 100; i++ {
			if _, ok := e.srv.voice.whoByID(id); ok {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		do(t, "POST", base+"/api/integration/phonebook/revoke", e.pb.cfg.key(pbKeyPhonebookToUCNG), jsonBody(map[string]any{"sessions": []string{c.Session}})).Body.Close()
		for i := 0; i < 100; i++ {
			if _, ok := e.srv.voice.whoByID(id); !ok {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("nach Abmeldung noch im Sprachkanal")
	})
}

func TestPhonebookVoiceAgents(t *testing.T) {
	e := phonebookServerWith(t, func(c *Config) { c.VoiceNoBuiltin = true; c.VoiceAgentToken = "agent-schluessel-1234567890" })
	base := e.ts.URL
	name := "talk"
	e.hub.CreateRoomWith(RoomInput{Name: &name, Voice: &VoiceConfig{}})
	tok, id := e.signIn(t, claimsFor("p_ag_anna", "anna", "Anna Berg", ""))
	if _, code := voiceJoin(t, base, tok, "talk"); code != http.StatusServiceUnavailable {
		t.Fatalf("ohne Sprach-Server: %d", code)
	}
	b, _ := json.Marshal(agentReport{ID: "voice1", WSURL: "wss://voice1.firma.local/ws", ControlURL: "http://voice1:8090/control",
		Stats: voicerelay.Stats{Participants: 3, Max: 200, MaxMbit: 50}, Rooms: map[string][]VoicePeer{}})
	if c := status(t, "POST", base+"/api/voice/agents/report", "falsch", string(b)); c != http.StatusUnauthorized {
		t.Fatalf("Sprach-Server mit falschem Schlüssel: %d", c)
	}
	if c := status(t, "POST", base+"/api/voice/agents/report", "agent-schluessel-1234567890", string(b)); c != 200 && c != 204 {
		t.Fatalf("Sprach-Server melden: %d", c)
	}
	j, code := voiceJoin(t, base, tok, "talk")
	if code != 200 || j.URL != "wss://voice1.firma.local/ws" {
		t.Fatalf("Zuweisung: %d %+v", code, j)
	}
	tk, err := voicerelay.VerifyTicket(e.srv.voice.pub, j.Ticket, time.Now())
	if err != nil || tk.User != id {
		t.Fatalf("Ticket: %+v %v", tk, err)
	}
	// CSP erlaubt den Sprach-Server; Einbettung bleibt verboten (ADR-019)
	res := do(t, "GET", base+"/", "", "")
	res.Body.Close()
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "wss://voice1.firma.local") || !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("CSP: %s", csp)
	}
}

func TestPhonebookTempRoomsBotsModeration(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	adm := adminLogin(t, base)
	anna, annaID := e.signIn(t, claimsFor("p_o_anna", "anna", "Anna Berg", ""))
	troll, trollID := e.signIn(t, claimsFor("p_o_troll", "troll", "Theo Roll", ""))
	msgs := base + "/api/rooms/lobby/messages"

	t.Run("Temporäre Kanäle", func(t *testing.T) {
		if ep, _ := say(t, base, anna, "lobby", "!kanal Projekt"); !ep.Ephemeral.Notice {
			t.Fatalf("ohne Recht: %+v", ep)
		}
		roles := decode[[]Role](t, expect(t, do(t, "PUT", base+"/api/admin/roles", adm, `{"roles":[{"name":"Projekte","color":"#336699","perms":{"tempRooms":true}}]}`), 200, "Rolle"))
		expect(t, do(t, "POST", base+"/api/admin/users/"+annaID+"/roles", adm, `{"roles":["`+roles[0].ID+`"]}`), 200, "Rolle vergeben").Body.Close()
		ep, _ := say(t, base, anna, "lobby", "!kanal Projekt Alpha")
		if ep.Ephemeral.Open != "projekt-alpha" {
			t.Fatalf("anlegen: %+v", ep)
		}
		if info, ok := e.hub.roomInfo("projekt-alpha"); !ok || info.Temp == nil || info.Temp.Owner != annaID {
			t.Fatalf("Besitz: %+v", info)
		}
		expect(t, do(t, "PATCH", base+"/api/rooms/projekt-alpha/settings", anna, `{"description":"Kurzes Projekt"}`), 200, "Besitzer-Einstellungen").Body.Close()
		expect(t, do(t, "PATCH", base+"/api/rooms/projekt-alpha/settings", troll, `{"description":"gekapert"}`), 403, "fremde Einstellungen").Body.Close()
	})

	t.Run("Bots: Bot-API, Sanktionen", func(t *testing.T) {
		_, botTok := makeBot(t, base, adm, `{"name":"Wache","allRooms":true,"perms":{"write":true,"delete":true,"mute":true,"maxMinutes":60}}`)
		expect(t, post(t, base+"/api/bot/rooms/lobby/messages", botTok, `{"text":"Guten Morgen vom Bot"}`), 201, "Bot schreibt").Body.Close()
		h := decode[[]Message](t, do(t, "GET", msgs+"?limit=5", anna, ""))
		if len(h) == 0 || !h[len(h)-1].Bot {
			t.Fatalf("Bot-Nachricht: %+v", h)
		}
		expect(t, do(t, "POST", base+"/api/bot/users/"+trollID+"/mute", botTok, `{"minutes":10}`), 200, "Bot schaltet stumm").Body.Close()
		if c := status(t, "POST", msgs, troll, `{"text":"darf ich?"}`); c != 403 {
			t.Fatalf("stumm geschaltet schreibt: %d", c)
		}
		expect(t, do(t, "DELETE", base+"/api/bot/users/"+trollID+"/sanction", botTok, ""), 204, "aufheben").Body.Close()
	})

	t.Run("Moderation: stummschalten, ausschließen", func(t *testing.T) {
		expect(t, do(t, "POST", base+"/api/admin/users/"+trollID+"/sanction", adm, `{"kind":"mute","minutes":30,"reason":"Spam"}`), 200, "mute").Body.Close()
		if res := post(t, msgs, troll, `{"text":"hallo"}`); res.StatusCode != 403 || !strings.Contains(errText(res), "Spam") {
			t.Fatal("stumm geschaltet")
		}
		expect(t, do(t, "DELETE", base+"/api/admin/users/"+trollID+"/sanction", adm, ""), 204, "aufheben").Body.Close()
		expect(t, do(t, "POST", base+"/api/admin/users/"+trollID+"/sanction", adm, `{"kind":"ban","minutes":0}`), 200, "ban").Body.Close()
		if res := post(t, msgs, troll, `{"text":"hallo?"}`); res.StatusCode != 403 {
			t.Fatalf("ausgeschlossen schreibt: %d", res.StatusCode)
		}
		// Ein neues Konto ist im Telefonbuch-Betrieb nicht möglich: dieselbe Person bekommt dasselbe Konto
		_, again := e.signIn(t, claimsFor("p_o_troll", "troll", "Theo Roll", ""))
		if again != trollID {
			t.Fatalf("Neuanmeldung umgeht den Ausschluss: %s ≠ %s", again, trollID)
		}
		if _, ok := e.hub.Mod().Active(trollID, ""); !ok {
			t.Fatal("Ausschluss nach Neuanmeldung weg")
		}
	})

	t.Run("Automoderation", func(t *testing.T) {
		expect(t, do(t, "PUT", base+"/api/admin/automod", adm, `{"enabled":true,"patterns":["schimpf*"]}`), 200, "Automod").Body.Close()
		m := decode[Message](t, expect(t, post(t, msgs, anna, `{"text":"so ein Schimpfwort"}`), 201, "senden"))
		if !m.Deleted || m.DeletedBy != "automod" {
			t.Fatalf("Automod: %+v", m)
		}
	})
}

func TestPhonebookPrivacyAndAdmin(t *testing.T) {
	e := phonebookServer(t)
	base := e.ts.URL
	adm := adminLogin(t, base)
	c := claimsFor("p_p_weg", "weg", "Wera Weg", "")
	tok, id := e.signIn(t, c)
	msgs := base + "/api/rooms/lobby/messages"
	post(t, msgs, tok, `{"text":"eins"}`).Body.Close()
	post(t, msgs, tok, `{"text":"zwei"}`).Body.Close()

	t.Run("Daten einer Person löschen", func(t *testing.T) {
		out := decode[map[string]any](t, expect(t, do(t, "POST", base+"/api/admin/users/"+id+"/purge", adm, `{"releaseName":true}`), 200, "löschen"))
		if out["deleted"].(float64) != 2 {
			t.Fatalf("%+v", out)
		}
		if h, _, _ := e.hub.History("lobby", 0, 50); len(h) != 0 {
			t.Fatalf("Nachrichten nicht gelöscht: %d", len(h))
		}
		// Anmeldung über das Telefonbuch funktioniert danach wieder (eigenes, sauberes Konto)
		c2 := claimsFor("p_p_weg", "weg", "Wera Weg", "")
		tok2, _ := e.signIn(t, c2)
		expect(t, post(t, msgs, tok2, `{"text":"wieder da"}`), 201, "nach dem Löschen").Body.Close()
	})

	t.Run("Statistik, Vorlagen, Sprachen, Ankündigungen", func(t *testing.T) {
		expect(t, do(t, "GET", base+"/api/admin/stats", adm, ""), 200, "Statistik").Body.Close()
		tpl := expect(t, do(t, "GET", base+"/api/admin/template?parts=roles,channels,settings", adm, ""), 200, "Vorlage exportieren")
		body, _ := io.ReadAll(tpl.Body)
		tpl.Body.Close()
		if len(body) == 0 || strings.Contains(string(body), "p_p_weg") || strings.Contains(string(body), "Wera") {
			t.Fatalf("Vorlage leer oder mit Personen (%d Bytes)", len(body))
		}
		expect(t, do(t, "GET", base+"/api/admin/i18n", adm, ""), 200, "Sprachen").Body.Close()
		sc := decode[Schedule](t, expect(t, do(t, "POST", base+"/api/admin/schedules", adm, `{"room":"lobby","text":"Wochenrückblick","mode":"daily","time":"08:00","enabled":true}`), 201, "Ankündigung"))
		expect(t, do(t, "POST", base+"/api/admin/schedules/"+sc.ID+"/run", adm, ""), 200, "jetzt senden").Body.Close()
		h, _, _ := e.hub.History("lobby", 0, 50)
		if len(h) == 0 || h[len(h)-1].Text != "Wochenrückblick" {
			t.Fatalf("Ankündigung: %+v", h)
		}
	})
}

// Vorlagen dürfen das Widget-Verbot nicht umgehen.
func TestPhonebookTemplateNoWidgets(t *testing.T) {
	e := phonebookServer(t)
	var res TplResult
	e.srv.tplWidgets([]byte(`[{"name":"Intranet","room":"lobby","domains":["intranet.example"]}]`), false, &res)
	if len(res.Errors) == 0 || len(e.hub.Widgets().List()) != 0 {
		t.Fatalf("Widget über Vorlage angelegt: %+v %d", res, len(e.hub.Widgets().List()))
	}
}
