package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// AD-Nachprüfung für das Telefonbuch selbst (RC; ADR-013 galt bisher nur für die Kommunikation).
//
// Sitzungen von AD-Konten gelten bis zu auth.sessionMax (Standard 12 h). Wird das Konto im AD
// gesperrt, gelöscht, aus der Anmeldegruppe genommen oder ändert sich die Admin-Berechtigung, soll
// das nicht erst beim nächsten Anmelden auffallen. Deshalb prüft das Telefonbuch das AD-Konto
// angemeldeter Personen höchstens alle auth.adRecheck (Standard 15 min) – im Hintergrund beim
// nächsten Seitenaufruf, damit kein Aufruf auf das AD warten muss.
//
//   - gesperrt/gelöscht/ohne Gruppe → alle Sitzungen der Person enden (auch in der Kommunikation)
//   - Admin-Recht geändert         → alle Sitzungen enden (neu anmelden mit den neuen Rechten)
//   - AD nicht erreichbar           → nichts ändert sich (das Telefonbuch bleibt bei einer
//     AD-Störung benutzbar; die Kommunikation sperrt selbst nach adGrace, ADR-013)

type adRecheckState struct {
	mu      sync.Mutex
	last    map[string]time.Time // Benutzername → letzte erfolgreiche Prüfung
	running map[string]bool
	lookup  func(username string) (*LDAPUser, error) // Tests: Ersatz für das AD
}

func (a *App) adLookup() func(string) (*LDAPUser, error) {
	a.adCheck.mu.Lock()
	defer a.adCheck.mu.Unlock()
	if a.adCheck.lookup != nil {
		return a.adCheck.lookup
	}
	if a.ldap == nil {
		return nil
	}
	return a.ldap.LookupUser
}

// maybeADRecheck startet die Prüfung, wenn sie fällig ist (nur AD-Sitzungen).
func (a *App) maybeADRecheck(rec *SessionRec, now time.Time) {
	if rec.Source != "ldap" {
		return
	}
	look := a.adLookup()
	if look == nil {
		return
	}
	key := strings.ToLower(rec.Username)
	a.adCheck.mu.Lock()
	if a.adCheck.last == nil {
		a.adCheck.last, a.adCheck.running = map[string]time.Time{}, map[string]bool{}
	}
	last, ok := a.adCheck.last[key]
	if !ok {
		last = rec.CreatedAt // Anmeldung = letzte Prüfung
	}
	if now.Sub(last) < a.cfg.Auth.ADRecheck.Duration || a.adCheck.running[key] {
		a.adCheck.mu.Unlock()
		return
	}
	a.adCheck.running[key] = true
	a.adCheck.mu.Unlock()
	go func() {
		defer func() {
			a.adCheck.mu.Lock()
			delete(a.adCheck.running, key)
			a.adCheck.mu.Unlock()
		}()
		a.adRecheck(rec.Username, rec.IsAdmin, look)
	}()
}

func (a *App) adRecheck(username string, wasAdmin bool, look func(string) (*LDAPUser, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lu, err := look(username)
	reason := ""
	switch {
	case errors.Is(err, errLDAPUserGone), errors.Is(err, errNotAllowed):
		reason = "im AD nicht mehr vorhanden oder ohne Anmeldegruppe"
	case err != nil:
		log.Printf("AD-Nachprüfung für %q nicht möglich: %v – Sitzungen bleiben bestehen", username, err)
		return
	case lu.Disabled:
		reason = "im AD gesperrt"
	case lu.IsAdmin != wasAdmin:
		reason = "Admin-Berechtigung im AD geändert"
	}
	a.adCheck.mu.Lock()
	a.adCheck.last[strings.ToLower(username)] = time.Now().UTC()
	a.adCheck.mu.Unlock()
	if reason == "" {
		return
	}
	if err := a.st().DeleteUserSessions(ctx, username, "ldap"); err != nil {
		log.Printf("AD-Nachprüfung: Sitzungen von %q nicht beendet: %v", username, err)
		return
	}
	a.audit(nil, "system", "ad_recheck_logout", username, reason)
	log.Printf("AD-Nachprüfung: %q %s – Sitzungen beendet", username, reason)
	if a.cfg.Communication.Enabled { // auch in der Kommunikation sofort (sonst ≤ Nachprüfung)
		if list, err := a.st().ListPrincipals(ctx); err == nil {
			var ids []string
			for _, p := range list {
				if p.Provider == "ldap" && strings.EqualFold(p.Username, username) {
					ids = append(ids, p.ID)
				}
			}
			if len(ids) > 0 && reason != "Admin-Berechtigung im AD geändert" {
				a.commRevoke(nil, ids)
			}
		}
	}
}
