// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpLocksValidate(t *testing.T) {
	for _, c := range []struct {
		mode  string
		locks OpLocks
		ok    bool
	}{
		{"", OpLocks{}, true},      // ohne Sperren: unverändert
		{"hostd", OpLocks{}, true}, // unbekannt ohne Sperren: wie bisher simple
		{"hosted", OpLocks{Guests: "none", NoRegistration: true, NoFederation: true, NoChannelFederation: true}, true},
		{"simple", OpLocks{NoChannelFederation: true}, true}, // Kanal-Föderation gibt es auch in simple
		{"simple", OpLocks{Guests: "none"}, false},           // simple: Gäste schreiben immer
		{"", OpLocks{NoRegistration: true}, false},           // Standard ist simple
		{"hostd", OpLocks{Guests: "none"}, false},            // Tippfehler darf nicht zu simple werden
		{"hostd", OpLocks{NoChannelFederation: true}, false}, // auch nicht bei anderen Sperren
		{"hosted", OpLocks{Guests: "write"}, false},          // write ist keine Sperre
		{"HOSTED ", OpLocks{Guests: "read"}, true},
	} {
		err := c.locks.validate(c.mode)
		if (err == nil) != c.ok {
			t.Errorf("validate(%q, %+v) = %v", c.mode, c.locks, err)
		}
	}
}

func lockedServer(t *testing.T, locks OpLocks) *hostedEnv {
	t.Helper()
	cfg := Config{MaxRooms: 5, RateLimit: 100, RateWindow: time.Second, ClaimLimit: 100, AdminPassword: testAdminPW, BotRate: 1000,
		Mode: ModeHosted, PublicURL: "https://chat.example.org", Locks: locks}
	hub := NewHub(cfg.MaxRooms)
	users := NewUsers(time.Hour, 100)
	clock := &testClock{t: time.Now()}
	users.now = clock.now
	srv := NewServer(cfg, hub, users)
	srv.acctRL = newRateLimiter(1000, time.Minute)
	srv.mailRL = newRateLimiter(1000, time.Minute)
	hub.EnsureDefaultRoom()
	ctx, cancel := context.WithCancel(context.Background())
	go srv.webhooks.run(ctx)
	t.Cleanup(cancel)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() { close(srv.done); ts.Close() })
	return &hostedEnv{ts, hub, srv, users, clock}
}

// Gate A der Phonebook-Integration: kein Gastzugang, keine offene Registrierung, keine Föderation –
// und der Admin-Bereich kann das nicht wieder öffnen.
func TestOpLocksNoGuestAccess(t *testing.T) {
	e := lockedServer(t, OpLocks{Guests: GuestsNone, NoRegistration: true, NoFederation: true, NoChannelFederation: true})
	base := e.ts.URL
	if out := jmap(t, do(t, "GET", base+"/api/settings", "", "")); out["hosted"].(map[string]any)["guests"] != "none" {
		t.Fatalf("Gäste-Regel: %v", out["hosted"])
	}
	for _, p := range []string{"/api/rooms", "/api/rooms/lobby/messages", "/api/rooms/lobby/events", "/api/profiles"} {
		if res := do(t, "GET", base+p, "", ""); res.StatusCode != 401 {
			t.Errorf("%s ohne Anmeldung: %d", p, res.StatusCode)
		}
	}
	if res := do(t, "POST", base+"/api/session", "", `{"name":"Gast"}`); res.StatusCode != 403 {
		t.Errorf("Gastname: %d", res.StatusCode)
	}
	if res := do(t, "POST", base+"/api/account/register", "", jsonBody(map[string]any{"name": "mallory", "keys": testKeys()})); res.StatusCode != 403 {
		t.Errorf("offene Registrierung: %d", res.StatusCode)
	}
	// .well-known bleibt erreichbar (Serverschlüssel), meldet aber Föderation aus
	if wk := jmap(t, do(t, "GET", base+"/.well-known/ucng", "", "")); wk["federation"] != false || wk["channels"] == true {
		t.Errorf(".well-known meldet Föderation: %v", wk)
	}
	adm := adminLogin(t, base)
	h := jmap(t, do(t, "GET", base+"/api/admin/hosted", adm, ""))
	if lk, _ := h["locks"].(map[string]any); lk == nil || lk["guests"] != "none" || lk["noRegistration"] != true {
		t.Fatalf("Sperren im Admin-Bereich nicht sichtbar: %v", h["locks"])
	}
	for _, body := range []string{
		`{"guests":"read","registration":false}`,
		`{"guests":"none","registration":true}`,
		`{"guests":"none","registration":false,"federation":{"enabled":true}}`,
	} {
		if res := do(t, "PUT", base+"/api/admin/hosted", adm, body); res.StatusCode != 409 {
			t.Errorf("Admin öffnet gesperrte Einstellung %s: %d", body, res.StatusCode)
		}
	}
	if res := do(t, "PUT", base+"/api/admin/hosted", adm, `{"guests":"none","registration":false,"smtp":{"port":587,"security":"starttls"}}`); res.StatusCode != 200 {
		t.Errorf("verträgliche Änderung abgelehnt: %d", res.StatusCode)
	}
	if res := do(t, "PUT", base+"/api/admin/channelfed", adm, `{"enabled":true}`); res.StatusCode != 409 {
		t.Errorf("Kanal-Föderation trotz Sperre: %d", res.StatusCode)
	}
	if res := do(t, "PUT", base+"/api/admin/channelfed", adm, `{"userSubs":true}`); res.StatusCode != 409 {
		t.Errorf("Kanal-Abos trotz Sperre: %d", res.StatusCode)
	}
	if e.srv.fedEnabled() || e.srv.chanFedOn() {
		t.Fatal("Föderation trotz Sperre an")
	}
	// Einladungen des Admins bleiben möglich (Testkonten bis PH-02); damit Lesen erlaubt
	inv := jmap(t, do(t, "POST", base+"/api/admin/invites", adm, `{"note":"Test"}`))
	link, _ := inv["link"].(string)
	tok := link[strings.Index(link, "invite=")+len("invite="):]
	res := do(t, "POST", base+"/api/account/register", "", jsonBody(map[string]any{"name": "anna", "keys": testKeys(), "invite": tok}))
	out := jmap(t, res)
	if res.StatusCode >= 300 {
		t.Fatalf("Registrierung mit Einladung: %d %v", res.StatusCode, out)
	}
	if res := do(t, "GET", base+"/api/rooms", sessionToken(out), ""); res.StatusCode != 200 {
		t.Fatalf("Konto liest nicht: %d", res.StatusCode)
	}
}

// Ohne Sperren bleibt alles beim Alten (Standalone).
func TestOpLocksOffUnchanged(t *testing.T) {
	e := hostedServer(t)
	adm := adminLogin(t, e.ts.URL)
	if res := do(t, "PUT", e.ts.URL+"/api/admin/hosted", adm, `{"guests":"write","registration":true,"smtp":{"port":587,"security":"starttls"}}`); res.StatusCode != 200 {
		t.Fatalf("ohne Sperren: %d", res.StatusCode)
	}
	if e.hub.Hosted().Config().Guests != "write" {
		t.Fatal("Einstellung nicht übernommen")
	}
}
