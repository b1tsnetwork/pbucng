// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Ende-zu-Ende-Verschlüsselung im Modus „phonebook“ (ADR-007, Variante A).
//
// Ohne UCNG-Passwort fehlt die bisherige Quelle des Schlüssels, der den privaten DM-Schlüssel
// schützt. Stattdessen legt jede Person einmalig ein eigenes Entsperr-Geheimnis fest. Der
// Browser leitet daraus – genau wie bisher aus dem Passwort – 512 Bit ab:
//   - auth (erste Hälfte) geht an den Server und wird dort gesalzen gehasht. Damit beweist der
//     Browser bei Änderungen, dass er das Geheimnis kennt;
//   - die zweite Hälfte verschlüsselt den privaten Schlüssel und verlässt den Browser nie.
//
// Zusätzlich gibt es einen Wiederherstellungscode (128 Bit Zufall, nur einmal angezeigt). Er
// verschlüsselt eine zweite Kopie des privaten Schlüssels. Wer das Geheimnis vergisst, setzt
// mit dem Code ein neues – ohne dass ältere Nachrichten verloren gehen. Wer beides verliert,
// kann nur neu anfangen (neues Schlüsselpaar, ältere verschlüsselte Nachrichten unlesbar).
//
// Der Server sieht weder Geheimnis noch Code noch den privaten Schlüssel im Klartext. Die
// Anmeldung über das Telefonbuch allein reicht nicht, um verschlüsselte Nachrichten zu lesen.

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"
)

type e2eRecovery struct {
	AuthSalt string `json:"authSalt"`
	AuthHash string `json:"authHash"`
	KDFSalt  string `json:"kdfSalt"`
	KDFIter  int    `json:"kdfIter"`
	PrivKey  string `json:"privKey"`
}

// RecoveryMaterial: Kopie des privaten Schlüssels unter dem Wiederherstellungscode.
type RecoveryMaterial struct {
	Auth    string `json:"auth"`
	KDFSalt string `json:"kdfSalt"`
	KDFIter int    `json:"kdfIter"`
	PrivKey string `json:"privKey"`
}

var (
	ErrE2EExists    = errors.New("Die Verschlüsselung ist bereits eingerichtet")
	ErrE2EMissing   = errors.New("Die Verschlüsselung ist noch nicht eingerichtet")
	ErrE2ESecret    = errors.New("Das Entsperr-Geheimnis stimmt nicht")
	ErrE2ERecovery  = errors.New("Der Wiederherstellungscode stimmt nicht")
	ErrE2EPubChange = errors.New("Der öffentliche Schlüssel darf sich dabei nicht ändern")
	ErrE2EStale     = errors.New("Bitte zuerst neu über das Telefonbuch anmelden (Sicherheitsabfrage)")
)

func (r RecoveryMaterial) validate() error {
	k := KeyMaterial{Auth: r.Auth, KDFSalt: r.KDFSalt, KDFIter: r.KDFIter}
	if err := k.validate(false); err != nil {
		return err
	}
	if b, err := base64.StdEncoding.DecodeString(r.PrivKey); err != nil || len(b) < 40 || len(b) > 1024 {
		return ErrKDF
	}
	return nil
}

func newE2ERecovery(r RecoveryMaterial) *e2eRecovery {
	var tmp member
	tmp.setAuth(KeyMaterial{Auth: r.Auth, KDFSalt: r.KDFSalt, KDFIter: r.KDFIter})
	return &e2eRecovery{AuthSalt: tmp.AuthSalt, AuthHash: tmp.AuthHash, KDFSalt: r.KDFSalt, KDFIter: r.KDFIter, PrivKey: r.PrivKey}
}

// extMemberLocked: Schattenkonto zum Token (nur Telefonbuch-Konten).
func (u *Users) extMemberLocked(token string) (*user, error) {
	x := u.memberOfTokenLocked(token)
	if x == nil || x.Member.Ext == nil || x.Member.Disabled || !u.memberSessionLiveLocked(x, hashToken(token)) {
		return nil, ErrNoMember
	}
	return x, nil
}

// E2ESetup: erstes Schlüsselpaar mit Entsperr-Geheimnis und Wiederherstellungskopie.
func (u *Users) E2ESetup(token string, k KeyMaterial, rec RecoveryMaterial) error {
	if err := k.validate(true); err != nil {
		return err
	}
	if err := rec.validate(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return err
	}
	m := x.Member
	if m.PubKey != "" {
		return ErrE2EExists
	}
	m.setAuth(k)
	m.PubKey, m.PrivKey = k.PubKey, k.PrivKey
	m.E2ERec = newE2ERecovery(rec)
	u.markDirty()
	return nil
}

// E2EChangeSecret: neues Entsperr-Geheimnis (gleiches Schlüsselpaar, privater Schlüssel neu verpackt).
func (u *Users) E2EChangeSecret(token, oldAuth string, k KeyMaterial) error {
	if err := k.validate(true); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return err
	}
	m := x.Member
	if m.PubKey == "" {
		return ErrE2EMissing
	}
	if !m.checkAuth(oldAuth) {
		return ErrE2ESecret
	}
	if k.PubKey != m.PubKey {
		return ErrE2EPubChange
	}
	m.setAuth(k)
	m.PrivKey = k.PrivKey
	u.markDirty()
	return nil
}

// E2ENewRecovery: neuer Wiederherstellungscode (verlangt das aktuelle Geheimnis).
func (u *Users) E2ENewRecovery(token, auth string, rec RecoveryMaterial) error {
	if err := rec.validate(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return err
	}
	m := x.Member
	if m.PubKey == "" {
		return ErrE2EMissing
	}
	if !m.checkAuth(auth) {
		return ErrE2ESecret
	}
	m.E2ERec = newE2ERecovery(rec)
	u.markDirty()
	return nil
}

// E2ERecoveryBlob: die mit dem Code verschlüsselte Kopie (zum Entschlüsseln im Browser).
func (u *Users) E2ERecoveryBlob(token string) (RecoveryMaterial, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return RecoveryMaterial{}, err
	}
	r := x.Member.E2ERec
	if r == nil || x.Member.PubKey == "" {
		return RecoveryMaterial{}, ErrE2EMissing
	}
	return RecoveryMaterial{KDFSalt: r.KDFSalt, KDFIter: r.KDFIter, PrivKey: r.PrivKey}, nil
}

// E2ERecover: mit dem Wiederherstellungscode ein neues Geheimnis setzen. Der Code ist danach
// verbraucht – der Browser liefert gleich einen neuen mit.
func (u *Users) E2ERecover(token, recAuth string, k KeyMaterial, rec RecoveryMaterial) error {
	if err := k.validate(true); err != nil {
		return err
	}
	if err := rec.validate(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return err
	}
	m := x.Member
	if m.PubKey == "" || m.E2ERec == nil {
		return ErrE2EMissing
	}
	if !checkAuthHash(m.E2ERec.AuthSalt, m.E2ERec.AuthHash, recAuth) {
		return ErrE2ERecovery
	}
	if k.PubKey != m.PubKey {
		return ErrE2EPubChange
	}
	m.setAuth(k)
	m.PrivKey = k.PrivKey
	m.E2ERec = newE2ERecovery(rec)
	u.markDirty()
	return nil
}

// E2EReset: Schlüssel verwerfen (Geheimnis und Code verloren). Nur kurz nach einer frischen
// Anmeldung über das Telefonbuch – ein gestohlenes, älteres Token reicht dafür nicht.
func (u *Users) E2EReset(token string, within time.Duration) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	x, err := u.extMemberLocked(token)
	if err != nil {
		return err
	}
	if !u.sessionFreshLocked(x, token, within) {
		return ErrE2EStale
	}
	m := x.Member
	m.PubKey, m.PrivKey, m.AuthSalt, m.AuthHash, m.KDFSalt, m.KDFIter, m.E2ERec = "", "", "", "", "", 0, nil
	u.markDirty()
	return nil
}

func e2eStatus(err error) int {
	switch {
	case errors.Is(err, ErrNoMember), errors.Is(err, ErrE2ESecret), errors.Is(err, ErrE2ERecovery):
		return http.StatusUnauthorized
	case errors.Is(err, ErrE2EExists), errors.Is(err, ErrE2EMissing), errors.Is(err, ErrE2EPubChange):
		return http.StatusConflict
	case errors.Is(err, ErrE2EStale):
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func e2eFail(w http.ResponseWriter, err error) {
	body := map[string]any{"error": err.Error()}
	if errors.Is(err, ErrE2EStale) {
		body["reauth"] = true
	}
	writeJSON(w, e2eStatus(err), body)
}

type e2eBody struct {
	Auth         string           `json:"auth"`         // aktuelles Geheimnis
	RecoveryAuth string           `json:"recoveryAuth"` // Wiederherstellungscode
	Keys         KeyMaterial      `json:"keys"`
	Recovery     RecoveryMaterial `json:"recovery"`
}

// POST /api/account/e2ee/{action}: setup, secret, recovery, recover, reset
func (s *Server) handleE2EE(w http.ResponseWriter, r *http.Request) {
	if !s.unlockRL.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "Zu viele Versuche – bitte eine Minute warten.")
		return
	}
	var in e2eBody
	if r.PathValue("action") != "reset" && !decodeJSON(w, r, &in) {
		return
	}
	tok := bearer(r)
	var err error
	switch r.PathValue("action") {
	case "setup":
		err = s.users.E2ESetup(tok, in.Keys, in.Recovery)
	case "secret":
		err = s.users.E2EChangeSecret(tok, in.Auth, in.Keys)
	case "recovery":
		err = s.users.E2ENewRecovery(tok, in.Auth, in.Recovery)
	case "recover":
		err = s.users.E2ERecover(tok, in.RecoveryAuth, in.Keys, in.Recovery)
	case "reset":
		err = s.users.E2EReset(tok, pbFreshForKeyReset)
	default:
		writeErr(w, http.StatusNotFound, "Unbekannte Aktion")
		return
	}
	if err != nil {
		e2eFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /api/account/e2ee/recovery – verschlüsselte Kopie für die Wiederherstellung
func (s *Server) handleE2EERecoveryGet(w http.ResponseWriter, r *http.Request) {
	if !s.unlockRL.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "Zu viele Versuche – bitte eine Minute warten.")
		return
	}
	rec, err := s.users.E2ERecoveryBlob(bearer(r))
	if err != nil {
		e2eFail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, rec)
}
