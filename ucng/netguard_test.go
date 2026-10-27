// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"image/color"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// PH-11: keine Anfragen ins interne Netz über Admin-Downloads und Webhooks (Modus phonebook).

func TestBlockedAddr(t *testing.T) {
	for _, c := range []struct {
		ip           string
		allowPrivate bool
		want         bool
	}{
		{"127.0.0.1", true, true}, {"::1", true, true}, {"169.254.169.254", true, true}, {"fe80::1", true, true},
		{"0.0.0.0", true, true}, {"224.0.0.1", true, true}, {"::ffff:127.0.0.1", true, true},
		{"10.1.2.3", false, true}, {"172.18.0.5", false, true}, {"192.168.1.1", false, true}, {"100.64.1.1", false, true}, {"fd00::1", false, true},
		{"10.1.2.3", true, false}, {"192.168.1.1", true, false},
		{"93.184.216.34", false, false}, {"2a00:1450:4001::1", false, false},
	} {
		if got := blockedAddr(netip.MustParseAddr(c.ip), c.allowPrivate); got != c.want {
			t.Errorf("%s (privat erlaubt: %v): %v, erwartet %v", c.ip, c.allowPrivate, got, c.want)
		}
	}
}

func TestPhonebookOutboundGuard(t *testing.T) {
	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG(32, 32, color.RGBA{1, 2, 3, 255}))
	}))
	defer internal.Close()
	e := phonebookServer(t)
	e.hub.Media().SetDir(t.TempDir())
	adm := adminLogin(t, e.ts.URL)
	res := do(t, "POST", e.ts.URL+"/api/admin/emotes", adm, `{"url":"`+internal.URL+`/x.png","name":"intern"}`)
	if res.StatusCode < 400 || hits.Load() != 0 {
		t.Fatalf("Download aus dem internen Netz: %d, Abrufe %d", res.StatusCode, hits.Load())
	}
	res.Body.Close()
	// Webhook an eine Loopback-Adresse wird nicht ausgeliefert
	_, _ = makeBot(t, e.ts.URL, adm, `{"name":"Haken","allRooms":true,"webhookUrl":"`+internal.URL+`/hook"}`)
	tok, _ := e.signIn(t, claimsFor("p_ng_anna", "anna", "Anna Berg", ""))
	expect(t, post(t, e.ts.URL+"/api/rooms/lobby/messages", tok, `{"text":"hallo Bot"}`), 201, "senden").Body.Close()
	time.Sleep(400 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("Webhook an Loopback ausgeliefert: %d", hits.Load())
	}
	// Standalone: unverändert (Downloads aus dem eigenen Netz gehen)
	ts, srv := testServerWith(t, nil)
	srv.hub.Media().SetDir(t.TempDir())
	adm2 := adminLogin(t, ts.URL)
	res = do(t, "POST", ts.URL+"/api/admin/emotes", adm2, `{"url":"`+internal.URL+`/x.png","name":"intern"}`)
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != 201 || hits.Load() != 1 {
		t.Fatalf("Standalone-Download: %d %v (Abrufe %d)", res.StatusCode, out, hits.Load())
	}
}
