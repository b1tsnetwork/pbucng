// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Austritt von Personen (Modus „phonebook“, PH-03, Entscheidung des Projektinhabers 26.10.2026):
//
//   - Meldet das Telefonbuch eine Person als gesperrt, gelöscht oder ohne Nutzungsrecht, wird
//     das Schattenkonto sofort gesperrt (alle Sitzungen enden, keine Rolle mehr) und als
//     „ausgeschieden seit“ markiert.
//   - Kommt die Person innerhalb der Frist zurück (wieder freigegeben), gilt das Konto wieder.
//   - Nach PHONEBOOK_RETENTION_DAYS (Standard 90) wird anonymisiert: Name → „Ehemalige Person …“
//     (auch in allen Nachrichten), Profilbild, Status, Schlüssel und die Verknüpfung mit dem
//     Telefonbuch werden gelöscht. Die Nachrichten selbst bleiben. Kommt die Person danach
//     zurück, bekommt sie ein neues Konto.
//
// Der Abgleich fragt regelmäßig (PHONEBOOK_LIFECYCLE, Standard 6h) den Status aller
// verknüpften Konten ab – auch derer ohne offene Sitzung.

import (
	"context"
	"log"
	"strconv"
	"time"
)

const (
	pbDefaultLifecycle = 6 * time.Hour
	pbDefaultRetention = 90
	pbFormerName       = "Ehemalige Person"
)

type pbStatus struct {
	Subject string `json:"subject"`
	Status  string `json:"status"` // active, disabled, forbidden, unknown
}

func (p *pbClient) principals(ctx context.Context, subjects []string) ([]pbStatus, error) {
	var out struct {
		Results []pbStatus `json:"results"`
	}
	if err := p.post(ctx, "/api/internal/communication/principals", map[string]any{"subjects": subjects}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// ExtSubjects: alle verknüpften Konten (für den Status-Abgleich).
func (u *Users) ExtSubjects() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]string, 0, len(u.byExt))
	for _, x := range u.byExt {
		out = append(out, x.Member.Ext.Subject)
	}
	return out
}

// ExtStatus übernimmt den Status aus dem Telefonbuch. Liefert die Zahl neu gesperrter Konten.
func (u *Users) ExtStatus(res []pbStatus) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	n := 0
	for _, r := range res {
		x := u.byExt[extKey(pbProvider, r.Subject)]
		if x == nil {
			continue
		}
		e := x.Member.Ext
		switch r.Status {
		case "active":
			if !e.Gone.IsZero() {
				e.Gone = time.Time{}
				u.markDirty()
			}
		case "disabled", "forbidden", "unknown":
			if u.markGoneLocked(x, now) {
				n++
			}
		}
	}
	return n
}

// markGoneLocked sperrt ein Schattenkonto (Sitzungen weg, keine Rolle) und merkt sich, seit wann.
func (u *Users) markGoneLocked(x *user, now time.Time) bool {
	e := x.Member.Ext
	first := e.Gone.IsZero()
	if first {
		e.Gone = now
	}
	if len(x.Member.Sessions) > 0 || e.Role != "" || len(e.Groups) > 0 {
		for _, s := range x.Member.Sessions {
			delete(u.byToken, s.Hash)
		}
		x.Member.Sessions, e.Role, e.Groups = nil, "", nil
		u.sessionsChangedLocked(x, true)
	}
	u.markDirty()
	return first
}

// ExtAnonymizeDue anonymisiert Konten, die länger als retention ausgeschieden sind. Liefert
// ID → neuer Name (der Verlauf wird danach umbenannt).
func (u *Users) ExtAnonymizeDue(retention time.Duration) map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	out := map[string]string{}
	for key, x := range u.byExt {
		e := x.Member.Ext
		if e.Gone.IsZero() || now.Sub(e.Gone) < retention {
			continue
		}
		name := pbFormerName + " " + x.ID[:6]
		if other := u.byKey[nameKey(name)]; other != nil && other != x {
			name = pbFormerName + " " + x.ID
		}
		u.renameLocked(x, name)
		delete(u.byExt, key)
		for _, s := range x.Member.Sessions {
			delete(u.byToken, s.Hash)
		}
		x.Avatar, x.Status, x.StatusText, x.Pinned, x.Roles, x.Team = "", "", "", nil, nil, false
		x.Member = &member{Disabled: true, Former: now} // Schlüssel, Verknüpfung, Sitzungen weg
		u.markDirty()
		out[x.ID] = name
	}
	return out
}

// pbLifecycle: ein Durchgang Status-Abgleich und Anonymisierung.
func (s *Server) pbLifecycle(ctx context.Context) error {
	subjects := s.users.ExtSubjects()
	gone := 0
	for len(subjects) > 0 {
		n := min(len(subjects), pbMaxValidateBatch)
		res, err := s.pb.principals(ctx, subjects[:n])
		if err != nil {
			s.pb.note(err)
			return err // bei Ausfall nichts sperren oder löschen (die Sitzungen sichert die Frist ab)
		}
		gone += s.users.ExtStatus(res)
		subjects = subjects[n:]
	}
	renamed := s.users.ExtAnonymizeDue(time.Duration(s.cfg.Phonebook.RetentionDays) * 24 * time.Hour)
	for id, name := range renamed {
		s.hub.RenameUser(id, name)
	}
	if gone > 0 || len(renamed) > 0 {
		log.Printf("Telefonbuch: %d Konto/Konten ausgeschieden, %d anonymisiert", gone, len(renamed))
		s.hub.AuditLog().Add(AuditEntry{Actor: "system", Display: "Telefonbuch", Action: "phonebook.lifecycle",
			Detail: strconv.Itoa(gone) + " ausgeschieden, " + strconv.Itoa(len(renamed)) + " anonymisiert"})
	}
	return nil
}

func (s *Server) pbLifecycleLoop() {
	// erster Durchgang kurz nach dem Start, danach im eingestellten Abstand
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			_ = s.pbLifecycle(ctx)
			cancel()
			t.Reset(s.cfg.Phonebook.Lifecycle)
		}
	}
}
