// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Modus „phonebook“, PH-04: Einstieg und Navigation.
//
//   - Keine Einbettung in fremde Seiten (ADR-019): Die Kommunikation öffnet als eigene Seite auf
//     ihrer Subdomain. Im Modus phonebook senden alle Seiten frame-ancestors 'self' (und für
//     ältere Browser X-Frame-Options: SAMEORIGIN; /admin bleibt bei DENY). Das Widget ist nicht
//     betroffen – es wird per Skript eingebunden, nicht als iframe. Ohne Modus phonebook bleibt
//     alles wie bisher.
//   - Design-Adapter: Stehen Titel und Farben noch auf Werkseinstellung, übernimmt die
//     Kommunikation beim Start einmalig Name und Akzentfarbe des Telefonbuchs. Danach gilt, was
//     im Admin-Bereich eingestellt ist.
//   - Rücksprung: Die Oberfläche zeigt einen Link zurück ins Telefonbuch und auf Profilkarten
//     „Im Telefonbuch“ (Suche nach dem Namen aus dem Telefonbuch).

import "log"

const (
	pbDesignTitle  = "Kommunikation"
	pbDesignAccent = "#1f6f8b" // Akzentfarbe des Telefonbuchs (web/css/app.css, --accent)
)

func (s *Server) frameAncestors() string {
	if !s.phonebook() {
		return ""
	}
	return "; frame-ancestors 'self'"
}

// pbAdoptDesign: Werkseinstellungen → Erscheinungsbild des Telefonbuchs (einmalig, gespeichert).
func (s *Server) pbAdoptDesign() {
	if !s.phonebook() {
		return
	}
	cur := s.hub.Settings()
	if cur.Title != defaultSettings.Title || cur.Accent != defaultSettings.Accent || cur.Sidebar != defaultSettings.Sidebar {
		return // schon eingestellt – nichts überschreiben
	}
	cur.Title, cur.Accent = pbDesignTitle, pbDesignAccent
	if _, err := s.hub.SetSettings(cur); err != nil {
		log.Printf("Telefonbuch: Erscheinungsbild nicht übernommen: %v", err)
		return
	}
	log.Printf("Telefonbuch: Erscheinungsbild übernommen (Titel %q, Akzent %s) – änderbar im Admin-Bereich", pbDesignTitle, pbDesignAccent)
}

// pbSearchName: Suchbegriff fürs Telefonbuch (Anzeigename aus dem Telefonbuch), leer = kein Link.
func (x *user) pbSearchName() string {
	if x.Member == nil || x.Member.Ext == nil || !x.Member.Ext.Gone.IsZero() {
		return ""
	}
	if x.Member.Ext.Display != "" {
		return x.Member.Ext.Display
	}
	return x.Name
}
