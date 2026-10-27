package main

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// RC: AD-Nachprüfung angemeldeter Personen im Telefonbuch selbst.

func TestADRecheckSessions(t *testing.T) {
	a := testApp(t)
	h := a.routes(false)
	ctx := context.Background()
	var calls atomic.Int32
	var state atomic.Value // "ok", "disabled", "gone", "nogroup", "down", "admin"
	state.Store("ok")
	a.adCheck.lookup = func(u string) (*LDAPUser, error) {
		calls.Add(1)
		switch state.Load().(string) {
		case "disabled":
			return &LDAPUser{Username: u, Disabled: true}, nil
		case "gone":
			return nil, errLDAPUserGone
		case "nogroup":
			return nil, errNotAllowed
		case "down":
			return nil, errors.New("AD nicht erreichbar")
		case "admin":
			return &LDAPUser{Username: u, IsAdmin: true}, nil
		}
		return &LDAPUser{Username: u}, nil
	}
	session := func(user string, age time.Duration) *http.Cookie {
		t.Helper()
		raw := randomToken(32)
		now := time.Now().UTC()
		rec := &SessionRec{IDHash: sha256hex(raw), Username: user, DisplayName: user, Source: "ldap", CSRF: "x",
			CreatedAt: now.Add(-age), LastSeen: now, ExpiresAt: now.Add(time.Hour)}
		if err := a.st().CreateSession(ctx, rec); err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "tb_session", Value: raw}
	}
	alive := func(c *http.Cookie) bool {
		rec, _ := a.st().GetSession(ctx, sha256hex(c.Value))
		return rec != nil
	}
	waitGone := func(c *http.Cookie, what string) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if !alive(c) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: Sitzung lebt noch", what)
	}
	settle := func() {
		for i := 0; i < 50; i++ {
			a.adCheck.mu.Lock()
			n := len(a.adCheck.running)
			a.adCheck.mu.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// frisch angemeldet: keine Prüfung
	fresh := session("frisch", time.Minute)
	getWith(h, "/api/me", fresh, "10.0.0.5")
	settle()
	if calls.Load() != 0 {
		t.Fatalf("Prüfung trotz frischer Anmeldung: %d", calls.Load())
	}
	// fällig, alles in Ordnung: Sitzung bleibt, nächste Prüfung erst nach adRecheck
	ok := session("kmeier", time.Hour)
	getWith(h, "/api/me", ok, "10.0.0.5")
	settle()
	getWith(h, "/api/me", ok, "10.0.0.5")
	settle()
	if calls.Load() != 1 || !alive(ok) {
		t.Fatalf("Prüfung: %d, Sitzung %v", calls.Load(), alive(ok))
	}
	// AD nicht erreichbar: Telefonbuch bleibt benutzbar
	state.Store("down")
	down := session("ausfall", time.Hour)
	getWith(h, "/api/me", down, "10.0.0.5")
	settle()
	if !alive(down) {
		t.Fatal("Sitzung bei AD-Störung beendet")
	}
	// im AD gesperrt / gelöscht / ohne Anmeldegruppe / Admin-Recht geändert: alle Sitzungen der Person enden
	for _, c := range []struct{ state, user string }{{"disabled", "gesperrt"}, {"gone", "geloescht"}, {"nogroup", "ohnegruppe"}, {"admin", "befoerdert"}} {
		state.Store(c.state)
		s1, s2 := session(c.user, time.Hour), session(c.user, time.Hour)
		getWith(h, "/api/me", s1, "10.0.0.5")
		waitGone(s1, c.state)
		waitGone(s2, c.state+" (zweite Sitzung)")
	}
	// lokale Konten werden nicht im AD geprüft
	state.Store("gone")
	before := calls.Load()
	raw := randomToken(32)
	now := time.Now().UTC()
	a.st().CreateSession(ctx, &SessionRec{IDHash: sha256hex(raw), Username: "lokal", Source: "local", CSRF: "x", CreatedAt: now.Add(-time.Hour), LastSeen: now, ExpiresAt: now.Add(time.Hour)})
	getWith(h, "/api/me", &http.Cookie{Name: "tb_session", Value: raw}, "10.0.0.5")
	settle()
	if calls.Load() != before {
		t.Fatal("lokales Konto im AD geprüft")
	}
}
