// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// PH-04: Einstieg und Navigation (ADR-019, Design-Adapter, Rücksprung).

func TestPhonebookFraming(t *testing.T) {
	e := phonebookServer(t)
	for _, c := range []struct{ path, xfo string }{
		{"/", "SAMEORIGIN"}, {"/index.html", "SAMEORIGIN"}, {"/api/settings", "SAMEORIGIN"},
		{"/embed/chat.js", "SAMEORIGIN"}, {"/admin", "DENY"},
	} {
		res := do(t, "GET", e.ts.URL+c.path, "", "")
		res.Body.Close()
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s: CSP ohne frame-ancestors 'self': %q", c.path, csp)
		}
		if got := res.Header.Get("X-Frame-Options"); got != c.xfo {
			t.Errorf("%s: X-Frame-Options %q, erwartet %q", c.path, got, c.xfo)
		}
	}

	// Ohne Modus phonebook bleibt alles wie bisher (Standalone unverändert).
	ts, _ := testServer(t)
	res := do(t, "GET", ts.URL+"/", "", "")
	res.Body.Close()
	if csp := res.Header.Get("Content-Security-Policy"); strings.Contains(csp, "frame-ancestors") {
		t.Errorf("Standalone: frame-ancestors gesetzt: %q", csp)
	}
	if got := res.Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("Standalone: X-Frame-Options %q", got)
	}
}

func TestPhonebookDesignAdapter(t *testing.T) {
	e := phonebookServer(t)
	e.srv.pbAdoptDesign()
	st := e.hub.Settings()
	if st.Title != pbDesignTitle || st.Accent != pbDesignAccent {
		t.Fatalf("Werkseinstellung nicht übernommen: %q %s", st.Title, st.Accent)
	}
	var pub struct {
		Title     string           `json:"title"`
		Accent    string           `json:"accent"`
		Phonebook *PublicPhonebook `json:"phonebook"`
	}
	res := do(t, "GET", e.ts.URL+"/api/settings", "", "")
	json.NewDecoder(res.Body).Decode(&pub)
	res.Body.Close()
	if pub.Title != pbDesignTitle || pub.Phonebook == nil || pub.Phonebook.Home != e.pb.ts.URL+"/" {
		t.Fatalf("Einstellungen: %+v", pub)
	}

	// Vom Admin geändert: wird beim nächsten Start nicht überschrieben.
	st.Title, st.Accent = "Mein Chat", "#aa3300"
	if _, err := e.hub.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	e.srv.pbAdoptDesign()
	if got := e.hub.Settings(); got.Title != "Mein Chat" || got.Accent != "#aa3300" {
		t.Fatalf("Admin-Einstellung überschrieben: %q %s", got.Title, got.Accent)
	}
	// Nur der Titel geändert, Farbe noch Werk: ebenfalls nichts anfassen.
	st.Title, st.Accent = "Nur Titel", defaultAccent
	e.hub.SetSettings(st)
	e.srv.pbAdoptDesign()
	if got := e.hub.Settings(); got.Title != "Nur Titel" || got.Accent != defaultAccent {
		t.Fatalf("teilweise geänderte Einstellung überschrieben: %q %s", got.Title, got.Accent)
	}

	// Standalone: nie.
	_, sa := testServerWith(t, nil)
	sa.pbAdoptDesign()
	if got := sa.hub.Settings(); got.Title != defaultSettings.Title || got.Accent != defaultAccent {
		t.Fatalf("Standalone verändert: %q %s", got.Title, got.Accent)
	}
}

func TestPhonebookProfileLink(t *testing.T) {
	e := phonebookServer(t)
	tok, id := e.signIn(t, claimsFor("p_0000000000000000aaaa", "kmeier", "Katrin Meier", ""))

	if c := status(t, "GET", e.ts.URL+"/api/profiles", "", ""); c != http.StatusUnauthorized {
		t.Fatalf("Profile ohne Anmeldung: %d", c)
	}
	profiles := func() map[string]Profile {
		res := do(t, "GET", e.ts.URL+"/api/profiles", tok, "")
		defer res.Body.Close()
		out := map[string]Profile{}
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	if p := profiles()[id]; p.PB != "Katrin Meier" {
		t.Fatalf("Suchbegriff fürs Telefonbuch: %+v", p)
	}
	// Auch ohne Profilbilder (eigener Zweig in handleProfiles)
	up := e.hub.Media().Uploads()
	up.Avatars = false
	if _, err := e.hub.Media().SetUploads(up); err != nil {
		t.Fatal(err)
	}
	if p := profiles()[id]; p.PB != "Katrin Meier" {
		t.Fatalf("ohne Profilbilder: %+v", p)
	}

	// Ausgeschieden: kein Link mehr (und das Konto ist ohnehin gesperrt).
	if n := e.users.ExtStatus([]pbStatus{{Subject: "p_0000000000000000aaaa", Status: "disabled"}}); n != 1 {
		t.Fatalf("nicht gesperrt: %d", n)
	}
	if p, ok := e.users.ProfileMap()[id]; ok && p.PB != "" {
		t.Fatalf("Link trotz Austritt: %+v", p)
	}

	// Standalone: Profile ohne Telefonbuch-Feld.
	ts, _ := testServer(t)
	res := do(t, "GET", ts.URL+"/api/profiles", "", "")
	var raw map[string]map[string]any
	json.NewDecoder(res.Body).Decode(&raw)
	res.Body.Close()
	for _, p := range raw {
		if _, ok := p["pb"]; ok {
			t.Fatalf("Standalone liefert pb: %v", raw)
		}
	}
}
