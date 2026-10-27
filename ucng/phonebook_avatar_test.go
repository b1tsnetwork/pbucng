// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"image/color"
	"testing"
	"time"
)

// PH-06 (ADR-015): Startbild aus dem Telefonbuch, in der Kommunikation ersetzbar.

func TestPhonebookAvatar(t *testing.T) {
	e := phonebookServer(t)
	e.hub.Media().SetDir(t.TempDir())
	base := e.ts.URL
	avatarOf := func(id string) string { return e.users.ProfileMap()[id].Avatar }
	waitAvatar := func(id, what string, ok func(string) bool) string {
		t.Helper()
		for i := 0; i < 200; i++ {
			if a := avatarOf(id); ok(a) {
				return a
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: %q", what, avatarOf(id))
		return ""
	}
	e.pb.mu.Lock()
	e.pb.avatar["p_av_anna"] = testPNG(128, 128, color.RGBA{200, 120, 40, 255})
	e.pb.mu.Unlock()
	c := claimsFor("p_av_anna", "anna", "Anna Berg", "")
	c.Avatar = "h1"
	tok, id := e.signIn(t, c)
	first := waitAvatar(id, "Startbild nach der Anmeldung", func(a string) bool { return a != "" })

	// unverändert: kein erneuter Abruf bei der Nachprüfung
	cc := c
	cc.SessionExpires = time.Now().Add(8 * time.Hour)
	e.pb.set(c.Session, pbResult{Valid: true, Claims: &cc})
	e.srv.pbRevalidate(t.Context())
	e.pb.mu.Lock()
	hits := e.pb.avHits
	e.pb.mu.Unlock()
	if hits != 1 {
		t.Fatalf("Abrufe: %d (erwartet 1)", hits)
	}
	// neuer Avatar im Telefonbuch → neues Startbild
	e.pb.mu.Lock()
	e.pb.avatar["p_av_anna"] = testPNG(128, 128, color.RGBA{20, 120, 240, 255})
	e.pb.mu.Unlock()
	cc.Avatar = "h2"
	e.pb.set(c.Session, pbResult{Valid: true, Claims: &cc})
	e.srv.pbRevalidate(t.Context())
	second := waitAvatar(id, "neues Startbild", func(a string) bool { return a != "" && a != first })

	// eigenes Bild ersetzt das Startbild und bleibt auch bei neuem Telefonbuch-Avatar
	res := upload(t, base+"/api/profile/avatar", tok, nil, "ich.png", testPNG(300, 300, color.RGBA{0, 200, 0, 255}))
	if res.StatusCode != 200 {
		t.Fatalf("eigenes Bild: %d", res.StatusCode)
	}
	res.Body.Close()
	own := waitAvatar(id, "eigenes Bild", func(a string) bool { return a != second })
	cc.Avatar = "h3"
	e.pb.set(c.Session, pbResult{Valid: true, Claims: &cc})
	e.srv.pbRevalidate(t.Context())
	if a := avatarOf(id); a != own {
		t.Fatalf("eigenes Bild überschrieben: %q → %q", own, a)
	}
	// eigenes Bild entfernen → wieder das Startbild aus dem Telefonbuch
	if c := status(t, "DELETE", base+"/api/profile/avatar", tok, ""); c != 204 {
		t.Fatalf("entfernen: %d", c)
	}
	waitAvatar(id, "zurück zum Startbild", func(a string) bool { return a != "" && a != own })

	// ungültiges Bild aus dem Telefonbuch: nichts übernommen
	e.pb.mu.Lock()
	e.pb.avatar["p_av_ben"] = []byte("kein bild")
	e.pb.mu.Unlock()
	cb := claimsFor("p_av_ben", "bweber", "Ben Weber", "")
	cb.Avatar = "x1"
	_, idB := e.signIn(t, cb)
	e.srv.pbSyncAvatars(t.Context())
	if a := avatarOf(idB); a != "" {
		t.Fatalf("ungültiges Bild übernommen: %q", a)
	}
	// Profilbilder ausgeschaltet: nichts übernehmen
	up := e.hub.Media().Uploads()
	up.Avatars = false
	e.hub.Media().SetUploads(up)
	cl := claimsFor("p_av_carl", "cklein", "Carl Klein", "")
	cl.Avatar = "c1"
	e.pb.mu.Lock()
	e.pb.avatar["p_av_carl"] = testPNG(128, 128, color.RGBA{1, 2, 3, 255})
	e.pb.mu.Unlock()
	_, idC := e.signIn(t, cl)
	e.srv.pbSyncAvatars(t.Context())
	if a := e.users.ProfileMap()[idC].Avatar; a != "" {
		t.Fatalf("trotz ausgeschalteter Profilbilder: %q", a)
	}
}
