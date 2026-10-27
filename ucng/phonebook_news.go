// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Modus „phonebook“, PH-05:
//
//   - Direktnachricht aus der Visitenkarte im Telefonbuch: Das Telefonbuch öffnet die
//     Kommunikation mit #pbdm:<Principal>. Die Oberfläche fragt GET /api/integration/phonebook/user
//     (mit der eigenen Sitzung), welches Konto dazu gehört, und öffnet die Unterhaltung. Es gibt nur
//     Konten, die schon einmal angemeldet waren und nicht ausgeschieden sind.
//   - Zähler im Telefonbuch-Kopf (Entscheidung des Projektinhabers 27.10.2026: „neu seit letztem
//     Besuch“): Direktnachrichten und Erwähnungen nach der letzten Aktivität im Chat. Der
//     Gelesen-Stand bleibt im Browser; der Server speichert dafür nichts Neues. Das Telefonbuch
//     fragt die Zahlen serverseitig ab (POST /api/integration/phonebook/news, Schlüssel
//     „phonebook->ucng“) – Inhalte, Absender oder Kanäle verlassen UCNG dabei nicht.

import (
	"net/http"
	"strings"
	"time"
)

// ExtIdentity: Konto zu einem Principal (nicht ausgeschieden, nicht gesperrt).
func (u *Users) ExtIdentity(subject string) (Identity, time.Time, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x := u.byExt[extKey(pbProvider, subject)]
	if x == nil || x.Member == nil || x.Member.Disabled || !x.Member.Ext.Gone.IsZero() {
		return Identity{}, time.Time{}, false
	}
	return x.identity(), x.LastSeen, true
}

// HasKeys: hat das Konto Schlüssel für verschlüsselte Direktnachrichten?
func (u *Users) HasKeys(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	x := u.byID[id]
	return x != nil && x.Member != nil && x.Member.PubKey != ""
}

// NewsInfo: neue Direktnachrichten und Erwähnungen seit einem Zeitpunkt.
type NewsInfo struct {
	DMs      int       `json:"dms"`
	Mentions int       `json:"mentions"`
	Since    time.Time `json:"since"`
}

// NewsSince zählt Nachrichten anderer nach since: in Direktnachrichten alle, sonst nur
// Erwähnungen (@Name, @Rolle). Gelöschte, eigene und von blockierten Personen zählen nicht.
func (h *Hub) NewsSince(since time.Time, a *Access, userID, name string, roleNames []string) NewsInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := NewsInfo{Since: since}
	for _, r := range h.rooms {
		if !r.canRead(a) {
			continue
		}
		floor := r.dmFloor(userID)
		for i := len(r.history) - 1; i >= 0; i-- {
			m := r.history[i]
			if !m.Time.After(since) || m.ID <= floor {
				break
			}
			if m.Deleted || m.UserID == userID || (m.UserID != "" && h.blocks.Has(userID, m.UserID)) {
				continue
			}
			if r.kind == RoomDM {
				out.DMs++
			} else if Mentions(m.Text, name) || MentionsAny(m.Text, roleNames) {
				out.Mentions++
			}
		}
	}
	return out
}

// POST /api/integration/phonebook/news {"subject":"p_…"} (nur Telefonbuch)
func (s *Server) handlePBNews(w http.ResponseWriter, r *http.Request) {
	if !s.pbPeer(r) {
		if !s.acctLimit(w, r) {
			return
		}
		writeErr(w, http.StatusUnauthorized, "Nicht berechtigt")
		return
	}
	var in struct {
		Subject string `json:"subject"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	me, seen, ok := s.users.ExtIdentity(in.Subject)
	if !ok {
		writeJSON(w, http.StatusOK, NewsInfo{}) // (noch) kein Konto: nichts Neues
		return
	}
	a := &Access{ID: me.ID, Team: me.Team, Roles: me.Roles}
	writeJSON(w, http.StatusOK, s.hub.NewsSince(seen, a, me.ID, me.Name, s.hub.Roles().Names(me.Roles, true)))
}

// GET /api/integration/phonebook/user?subject=p_… (eigene Sitzung) – für #pbdm:<Principal>
func (s *Server) handlePBUser(w http.ResponseWriter, r *http.Request) {
	me, ok := s.users.Lookup(bearer(r))
	if !ok || !me.Member {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": ErrLoginRequired.Error(), "login": true})
		return
	}
	subject := r.URL.Query().Get("subject")
	if !strings.HasPrefix(subject, "p_") || len(subject) > 64 {
		writeErr(w, http.StatusBadRequest, "Ungültige Angabe")
		return
	}
	other, _, ok := s.users.ExtIdentity(subject)
	if !ok {
		writeErr(w, http.StatusNotFound, ErrPBNotYet.Error())
		return
	}
	if other.ID == me.ID {
		writeErr(w, http.StatusBadRequest, ErrPBSelf.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": other.ID, "name": other.Name, "e2e": s.users.HasKeys(other.ID)})
}
