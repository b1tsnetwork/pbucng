// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Betriebssperren (Phonebook-Integration, ADR-009): Einstellungen, die der Betrieb per Umgebung
// festlegt und die der Admin-Bereich nicht überschreiben kann. Gedacht für den Betrieb als
// Kommunikations-Add-on, in dem kein Gastzugang, keine offene Registrierung und keine Föderation
// erlaubt sind. Ohne gesetzte Sperren verhält sich UCNG unverändert.
//
//	LOCK_GUESTS=none|read        Gäste-Regel fest (nur mit MODE=hosted)
//	LOCK_REGISTRATION=1          keine offene Registrierung (Einladungen des Admins bleiben möglich)
//	LOCK_FEDERATION=1            Föderation von Direktnachrichten aus
//	LOCK_CHANNEL_FEDERATION=1    Kanal-Föderation aus (auch Abos durch Personen)
//	LOCK_BEACON=1                kein Beacon: kein Verzeichniseintrag, keine GIF-Sammlungen (RC, ADR-009)
//	LOCK_EXTERNAL_GIFS=1         keine GIF-Suche bei GIPHY/Tenor (RC, ADR-009)

import (
	"errors"
	"fmt"
	"strings"
)

type OpLocks struct {
	Guests              string `json:"guests,omitempty"` // "", none, read
	NoRegistration      bool   `json:"noRegistration,omitempty"`
	NoFederation        bool   `json:"noFederation,omitempty"`
	NoChannelFederation bool   `json:"noChannelFederation,omitempty"`
	NoBeacon            bool   `json:"noBeacon,omitempty"`
	NoExternalGIFs      bool   `json:"noExternalGifs,omitempty"`
	conflict            string // Modus phonebook: widersprüchliche LOCK_GUESTS-Angabe
}

// forPhonebook: Im Modus phonebook gelten alle Sperren fest – kein Gastzugang, keine
// Registrierung, keine Föderation (Fahrplan PH-02). Eine abweichende LOCK_GUESTS-Angabe ist ein
// Fehler und wird nicht still überschrieben.
func (l OpLocks) forPhonebook() OpLocks {
	out := OpLocks{Guests: GuestsNone, NoRegistration: true, NoFederation: true, NoChannelFederation: true, NoBeacon: true, NoExternalGIFs: true}
	if l.Guests != "" && l.Guests != GuestsNone {
		out.conflict = l.Guests
	}
	return out
}

func (l OpLocks) Any() bool {
	return l.Guests != "" || l.NoRegistration || l.NoFederation || l.NoChannelFederation || l.NoBeacon || l.NoExternalGIFs
}

// Names: aktive Sperren (überprüfbar in /api/settings, ADR-020).
func (l OpLocks) Names() []string {
	out := []string{}
	for _, x := range []struct {
		on   bool
		name string
	}{{l.Guests != "", "guests"}, {l.NoRegistration, "registration"}, {l.NoFederation, "federation"},
		{l.NoChannelFederation, "channelFederation"}, {l.NoBeacon, "beacon"}, {l.NoExternalGIFs, "externalGifs"}} {
		if x.on {
			out = append(out, x.name)
		}
	}
	return out
}

// validate: Sperren müssen zur Betriebsart passen. rawMode ist MODE, wie er gesetzt wurde –
// unbekannte Werte werden sonst still zu „simple“, und dort dürfen Gäste immer schreiben.
func (l OpLocks) validate(rawMode string) error {
	switch l.Guests {
	case "", GuestsNone, GuestsRead:
	default:
		return fmt.Errorf("LOCK_GUESTS: none oder read (nicht %q)", l.Guests)
	}
	m := strings.ToLower(strings.TrimSpace(rawMode))
	if m == ModePhonebook {
		if l.conflict != "" {
			return fmt.Errorf("MODE=phonebook erlaubt keinen Gastzugang (LOCK_GUESTS=%s)", l.conflict)
		}
		if l.Guests != GuestsNone || !l.NoRegistration || !l.NoFederation || !l.NoChannelFederation || !l.NoBeacon || !l.NoExternalGIFs {
			return errors.New("MODE=phonebook: Sperren unvollständig")
		}
		return nil
	}
	if m != "" && m != ModeSimple && m != ModeHosted {
		if l.Any() {
			return fmt.Errorf("MODE=%q ist unbekannt – mit Betriebssperren wird nicht auf „simple“ ausgewichen", rawMode)
		}
	}
	if (l.Guests != "" || l.NoRegistration || l.NoFederation) && m != ModeHosted {
		return errors.New("LOCK_GUESTS, LOCK_REGISTRATION und LOCK_FEDERATION gelten nur mit MODE=hosted – im Modus simple dürfen Gäste immer schreiben")
	}
	return nil
}

func (l OpLocks) applyHosted(c HostedConfig) HostedConfig {
	if l.Guests != "" {
		c.Guests = l.Guests
	}
	if l.NoRegistration {
		c.Registration = false
	}
	if l.NoFederation {
		c.Fed.Enabled = false
	}
	return c
}

func (l OpLocks) applyChanFed(c ChanFedConfig) ChanFedConfig {
	if l.NoChannelFederation {
		c.Enabled, c.UserSubs = false, false
	}
	return c
}

// ErrLocked: Der Admin-Bereich versucht, eine gesperrte Einstellung zu ändern.
var ErrLocked = errors.New("Diese Einstellung ist vom Betrieb gesperrt (Umgebungsvariable LOCK_…) und lässt sich im Admin-Bereich nicht ändern")

// checkHosted: Änderungen im Admin-Bereich, die einer Sperre widersprechen, ablehnen statt still zu überschreiben.
func (l OpLocks) checkHosted(in HostedConfig) error {
	if l.Guests != "" && in.Guests != "" && in.Guests != l.Guests {
		return ErrLocked
	}
	if (l.NoRegistration && in.Registration) || (l.NoFederation && in.Fed.Enabled) {
		return ErrLocked
	}
	return nil
}

func (l OpLocks) checkChanFed(in ChanFedConfig) error {
	if l.NoChannelFederation && (in.Enabled || in.UserSubs) {
		return ErrLocked
	}
	return nil
}

func parseLockBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "ja", "on":
		return true
	}
	return false
}

// SetLocks hinterlegt die Betriebssperren; Config() liefert ab dann die gesperrten Werte.
func (h *Hosted) SetLocks(l OpLocks) {
	h.mu.Lock()
	h.locks = l
	h.mu.Unlock()
}

func (c *ChanFed) SetLocks(l OpLocks) {
	c.mu.Lock()
	c.locks = l
	c.mu.Unlock()
}
