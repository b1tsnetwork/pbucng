// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Schattenkonten für den Modus „phonebook“ (siehe phonebook.go).
//
// Ein Schattenkonto ist ein normales Konto (member) ohne Passwort. Die Verknüpfung mit dem
// Telefonbuch steht in member.Ext: Anbieter + stabile principal_id (eindeutig, Index byExt).
// Der Anmeldename verknüpft nie – ein gleichnamiges lokales Konto wird nicht übernommen.
//
// Jede Sitzung trägt den Bezug zur Telefonbuch-Sitzung (PB), deren Ablauf (Until) und den
// Zeitpunkt der letzten erfolgreichen Prüfung (Checked). Ohne Prüfung innerhalb der Frist
// (extGrace) gilt sie nicht mehr (fail-closed); sie wird aber erst entfernt, wenn das Telefonbuch
// sie für ungültig erklärt – so geht nach einem kurzen Ausfall nichts verloren.

import (
	"fmt"
	"strings"
	"time"
)

type extIdentity struct {
	Provider string    `json:"provider"`
	Subject  string    `json:"subject"`
	Username string    `json:"username"`
	Display  string    `json:"display,omitempty"`
	Role     string    `json:"role,omitempty"`
	Groups   []string  `json:"groups,omitempty"` // pb:org:<id>, pb:lead:<id> (Zugriffsrollen)
	Linked   time.Time `json:"linked"`
	Gone     time.Time `json:"gone,omitempty"` // ausgeschieden seit (Telefonbuch: gesperrt/gelöscht/ohne Recht)
	// PH-06 (ADR-015): Startbild aus dem Telefonbuch
	AvatarWant string    `json:"avatarWant,omitempty"` // Prüfsumme laut Telefonbuch
	AvatarPB   string    `json:"avatarPb,omitempty"`   // übernommene Prüfsumme
	AvatarOwn  bool      `json:"avatarOwn,omitempty"`  // eigenes Bild hochgeladen – Telefonbuch-Bild nicht mehr übernehmen
	Checked    time.Time `json:"checked,omitempty"`
}

// ExtView: Sicht auf die Verknüpfung (eigenes Konto, Admin-Bereich).
type ExtView struct {
	Provider string `json:"provider"`
	Username string `json:"username"`
	Role     string `json:"role,omitempty"`
}

func extKey(provider, subject string) string { return provider + "|" + subject }

// SetExtGrace schaltet die Prüfung der Telefonbuch-Sitzungen ein (0 = aus).
func (u *Users) SetExtGrace(d time.Duration) {
	u.mu.Lock()
	u.extGrace = d
	u.mu.Unlock()
}

// sessionLiveLocked: gilt eine Sitzung (noch)? Nur Telefonbuch-Sitzungen haben Fristen.
func (u *Users) sessionLiveLocked(s *memberSession) bool {
	if s.PB == "" {
		// Modus phonebook: Sitzungen ohne Telefonbuch-Bezug (z. B. Passwort-Anmeldung aus der
		// Zeit als hosted) gelten nicht mehr.
		return u.extGrace <= 0
	}
	now := u.now()
	if !s.Until.IsZero() && now.After(s.Until) {
		return false
	}
	return u.extGrace <= 0 || now.Sub(s.Checked) <= u.extGrace
}

// extNames: Namensvorschläge in der Reihenfolge, in der sie versucht werden. Geprüft gegen
// Bot- und geschützte Namen (außerhalb der Sperre, siehe Register).
func (u *Users) extNames(c pbClaims) []string {
	var cand []string
	add := func(n string) {
		if n, ok := CleanName(n); ok && len(n) >= 2 {
			for _, x := range cand {
				if nameKey(x) == nameKey(n) {
					return
				}
			}
			cand = append(cand, n)
		}
	}
	disp := strings.TrimSpace(c.DisplayName)
	add(disp)
	if disp != "" {
		add(fmt.Sprintf("%s (%s)", disp, c.Username))
	}
	add(c.Username)
	f := u.reserved.Load()
	var out []string
	for _, n := range cand {
		if f == nil || (*f)(n, false) == nil {
			out = append(out, n)
		}
	}
	return out
}

// pickNameLocked: erster freier Name; sonst mit Zahl.
func (u *Users) pickNameLocked(cand []string, self *user, fallback string) string {
	free := func(n string) bool {
		x := u.byKey[nameKey(n)]
		return x == nil || x == self || u.expired(x)
	}
	for _, n := range cand {
		if free(n) {
			return n
		}
	}
	base := fallback
	if len(cand) > 0 {
		base = cand[len(cand)-1]
	} else if free(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); free(n) {
			return n
		}
	}
	return fmt.Sprintf("%s-%s", base, newUserID())
}

// renameLocked ändert den Namen eines Schattenkontos (Anzeigename im Telefonbuch geändert).
func (u *Users) renameLocked(x *user, name string) {
	if x.Name == name {
		return
	}
	if old := u.byKey[nameKey(x.Name)]; old == x {
		delete(u.byKey, nameKey(x.Name))
	}
	if other := u.byKey[nameKey(name)]; other != nil && other != x { // abgelaufener Gastname
		u.removeLocked(other)
	}
	x.Name = name
	u.byKey[nameKey(name)] = x
}

// ExtLogin: Anmeldung über das Telefonbuch. Legt das Schattenkonto bei Bedarf an und stellt eine
// Sitzung aus, die an die Telefonbuch-Sitzung gebunden ist.
func (u *Users) ExtLogin(c pbClaims, agent string) (LoginResult, bool, error) {
	if !c.valid() {
		return LoginResult{}, false, ErrPBClaims
	}
	cand := u.extNames(c)
	// alle Vorschläge geschützt (z. B. „admin“): neutraler Name mit Kurzkennung
	short := strings.TrimPrefix(c.Subject, "p_")
	if len(short) > 6 {
		short = short[:6]
	}
	fallback := "Konto " + short
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	key := extKey(pbProvider, c.Subject)
	x := u.byExt[key]
	created, renamed := false, false
	if x == nil {
		if len(u.byKey) >= u.max {
			u.expireLocked()
			if len(u.byKey) >= u.max {
				return LoginResult{}, false, ErrTooManyUsers
			}
		}
		x = &user{ID: newUserID(), TokenHash: "acct:" + newToken(), Created: now, LastSeen: now,
			Member: &member{Ext: &extIdentity{Provider: pbProvider, Subject: c.Subject, Linked: now}}}
		x.Name = u.pickNameLocked(cand, x, fallback)
		u.byKey[nameKey(x.Name)] = x
		u.byID[x.ID] = x
		u.byExt[key] = x
		created = true
	} else {
		if x.Member.Disabled { // in UCNG zusätzlich gesperrt
			return LoginResult{}, false, ErrDisabled
		}
		keep := false // bisheriger Name passt noch zu einem Vorschlag: nicht umbenennen
		for _, n := range cand {
			if nameKey(n) == nameKey(x.Name) {
				keep = true
			}
		}
		if !keep {
			old := x.Name
			u.renameLocked(x, u.pickNameLocked(cand, x, fallback))
			renamed = old != x.Name
		}
	}
	e := x.Member.Ext
	e.Username, e.Display, e.Role, e.Checked, e.Gone = c.Username, c.DisplayName, c.Role, now, time.Time{}
	e.AvatarWant = c.Avatar
	u.setExtGroupsLocked(x, c.Groups)
	tok := u.newSessionLocked(x, agent)
	s := &x.Member.Sessions[len(x.Member.Sessions)-1]
	s.PB, s.Until, s.Checked = c.Session, c.SessionExpires, now
	u.markDirty()
	m := x.Member
	return LoginResult{Token: tok, ID: x.ID, Name: x.Name, PubKey: m.PubKey, PrivKey: m.PrivKey, Renamed: renamed}, created, nil
}

// ExtSessions: alle Telefonbuch-Sitzungen zur Nachprüfung. active = in der letzten Runde benutzt.
func (u *Users) ExtSessions(every time.Duration) []pbCheck {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	seen := map[string]bool{}
	var out []pbCheck
	for _, x := range u.byExt {
		for _, s := range x.Member.Sessions {
			if s.PB == "" || seen[s.PB] {
				continue
			}
			seen[s.PB] = true
			out = append(out, pbCheck{Session: s.PB, Subject: x.Member.Ext.Subject, Active: now.Sub(s.LastUsed) <= every+time.Minute})
		}
	}
	return out
}

// ExtApply übernimmt das Ergebnis einer Nachprüfung. Sitzungen, zu denen das Telefonbuch nichts
// sagt, bleiben unverändert (sie laufen dann über die Frist ab).
func (u *Users) ExtApply(asked []pbCheck, res []pbResult) {
	bySess := map[string]pbResult{}
	for _, r := range res {
		bySess[r.Session] = r
	}
	asking := map[string]bool{}
	for _, a := range asked {
		asking[a.Session] = true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for _, x := range u.byExt {
		m := x.Member
		var rest []memberSession
		changed, cut := false, false
		for _, s := range m.Sessions {
			r, ok := bySess[s.PB]
			if s.PB == "" || !ok || !asking[s.PB] {
				rest = append(rest, s)
				continue
			}
			if !r.Valid || (r.Claims != nil && r.Claims.Subject != m.Ext.Subject) {
				delete(u.byToken, s.Hash)
				changed = true
				if r.Reason == "disabled" || r.Reason == "forbidden" {
					cut = true
				}
				continue
			}
			s.Checked = now
			if r.Claims != nil && r.Claims.valid() {
				s.Until = r.Claims.SessionExpires
				m.Ext.Username, m.Ext.Display, m.Ext.Role, m.Ext.Checked = r.Claims.Username, r.Claims.DisplayName, r.Claims.Role, now
				m.Ext.AvatarWant = r.Claims.Avatar
				u.setExtGroupsLocked(x, r.Claims.Groups)
			}
			rest = append(rest, s)
		}
		m.Sessions = rest
		if cut { // Konto im Telefonbuch gesperrt oder ohne Nutzungsrecht: sperren (Austritt, PH-03)
			m.Sessions = rest
			u.markGoneLocked(x, now)
			continue
		}
		if changed {
			u.sessionsChangedLocked(x, false)
		}
	}
	u.markDirty()
}

// ExtRevoke beendet Sitzungen (Abmeldung im Telefonbuch) bzw. alle Sitzungen von Personen
// (Sperre). Liefert die Zahl beendeter Sitzungen.
func (u *Users) ExtRevoke(sessions, subjects []string) int {
	ends := map[string]bool{}
	for _, s := range sessions {
		ends[s] = true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, subj := range subjects {
		if x := u.byExt[extKey(pbProvider, subj)]; x != nil {
			n += len(x.Member.Sessions)
			u.markGoneLocked(x, u.now()) // Sperre im Telefonbuch/AD: Austritt (PH-03)
		}
	}
	if len(ends) > 0 {
		for _, x := range u.byExt {
			var rest []memberSession
			hit := false
			for _, s := range x.Member.Sessions {
				if s.PB != "" && ends[s.PB] {
					delete(u.byToken, s.Hash)
					n++
					hit = true
					continue
				}
				rest = append(rest, s)
			}
			if hit {
				x.Member.Sessions = rest
				u.sessionsChangedLocked(x, false)
			}
		}
	}
	if n > 0 {
		u.markDirty()
	}
	return n
}

// ExtStale: Gehört das Token zu einer Telefonbuch-Sitzung, die nur deshalb nicht gilt, weil die
// Nachprüfung länger als die Frist ausblieb (Telefonbuch nicht erreichbar)? Solche Sitzungen
// kommen nach der nächsten erfolgreichen Nachprüfung zurück – die Oberfläche soll sie deshalb
// nicht verwerfen (503 statt 401). Widerrufene, abgelaufene oder gesperrte Sitzungen: false.
func (u *Users) ExtStale(token string) bool {
	if token == "" || u.extGrace <= 0 {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	h := hashToken(token)
	x := u.byToken[h]
	if x == nil || x.Member == nil || x.Member.Ext == nil || x.Member.Disabled {
		return false
	}
	now := u.now()
	for i := range x.Member.Sessions {
		s := &x.Member.Sessions[i]
		if s.Hash == h {
			return s.PB != "" && (s.Until.IsZero() || !now.After(s.Until)) && now.Sub(s.Checked) > u.extGrace
		}
	}
	return false
}

// ExtOf: Verknüpfung eines Kontos, nur solange die angegebene Sitzung gilt.
func (u *Users) ExtOf(id, sid string) (ExtView, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x := u.byID[id]
	if x == nil || x.Member == nil || x.Member.Ext == nil || x.Member.Disabled {
		return ExtView{}, false
	}
	for i := range x.Member.Sessions {
		s := &x.Member.Sessions[i]
		if sessionID(s.Hash) == sid && s.PB != "" && u.sessionLiveLocked(s) {
			e := x.Member.Ext
			return ExtView{Provider: e.Provider, Username: e.Username, Role: e.Role}, true
		}
	}
	return ExtView{}, false
}

// HasLiveSession: hat ein Konto mindestens eine gültige Sitzung? (Sprachkanäle im Modus phonebook)
func (u *Users) HasLiveSession(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	x := u.byID[id]
	if x == nil || x.Member == nil || x.Member.Disabled {
		return false
	}
	for i := range x.Member.Sessions {
		if u.sessionLiveLocked(&x.Member.Sessions[i]) {
			return true
		}
	}
	return false
}

// sessionFresh: wurde die Sitzung des Tokens vor kurzem ausgestellt? (Schlüssel zurücksetzen)
func (u *Users) sessionFreshLocked(x *user, token string, within time.Duration) bool {
	h := hashToken(token)
	for _, s := range x.Member.Sessions {
		if s.Hash == h {
			return u.now().Sub(s.Created) <= within
		}
	}
	return false
}

// ExtSweep schließt Streams und Sprachverbindungen von Sitzungen, deren Frist abgelaufen ist
// (Telefonbuch zu lange nicht erreichbar oder Telefonbuch-Sitzung abgelaufen).
func (u *Users) ExtSweep() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.byExt {
		for i := range x.Member.Sessions {
			if !u.sessionLiveLocked(&x.Member.Sessions[i]) {
				u.sessionsChangedLocked(x, false)
				break
			}
		}
	}
}

// OnAccessChanged: Gruppen einer Person haben sich geändert (offene Verbindungen nachziehen).
func (u *Users) OnAccessChanged(f func(userID string, team bool, roles []string)) {
	u.accessChanged.Store(&f)
}

// setExtGroupsLocked übernimmt die Gruppen aus dem Telefonbuch; bei Änderung werden offene
// Verbindungen geprüft (Kanäle, die nicht mehr lesbar sind, werden getrennt).
func (u *Users) setExtGroupsLocked(x *user, groups []string) {
	g := pbGroups(groups)
	e := x.Member.Ext
	if strings.Join(g, ",") == strings.Join(e.Groups, ",") {
		return
	}
	e.Groups = g
	u.markDirty()
	if f := u.accessChanged.Load(); f != nil {
		id, team, roles := x.ID, x.Team, x.accessRoles()
		go (*f)(id, team, roles)
	}
}

// accessRoles: Rollen für Kanalrechte – eigene Rollen und Gruppen aus dem Telefonbuch.
func (x *user) accessRoles() []string {
	out := append([]string(nil), x.Roles...)
	if x.Member != nil && x.Member.Ext != nil {
		out = append(out, x.Member.Ext.Groups...)
	}
	return out
}
