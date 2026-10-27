// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Modus „phonebook“ (Phonebook-Integration, PH-02): UCNG als Kommunikations-Add-on eines
// Telefonbuchs. Die Anmeldung übernimmt das Telefonbuch; UCNG hat kein eigenes Passwort, keine
// Registrierung, keine Einladungen und keinen Gastzugang.
//
// Ablauf der Anmeldung (Authorization Code mit PKCE, ADR-005/006):
//
//  1. Der Browser erzeugt state und code_verifier, merkt sie sich (sessionStorage) und geht zu
//     PHONEBOOK_URL/api/addons/communication/authorize?state=…&challenge=S256(verifier).
//  2. Das Telefonbuch prüft seine eigene Sitzung und leitet mit einem einmaligen Code (60 s)
//     zurück: <UCNG>/#pb_code=…&pb_state=… (Fragment – erscheint in keinem Server-Protokoll).
//  3. Der Browser prüft state und schickt Code + verifier an POST /api/integration/phonebook/exchange.
//  4. UCNG löst den Code serverseitig beim Telefonbuch ein (gemeinsames Geheimnis) und bekommt
//     die Identität (stabile principal_id, Anzeigename, Rolle, Sitzungsbezug).
//  5. UCNG legt bei Bedarf ein Schattenkonto an (ohne Passwort) und stellt eine Sitzung aus.
//
// Widerruf (ADR-013, ADR-018): Das Telefonbuch meldet Abmeldungen und Sperren aktiv
// (POST /api/integration/phonebook/revoke). Zusätzlich prüft UCNG alle PHONEBOOK_REVALIDATE
// jede Sitzung nach. Ist das Telefonbuch länger als PHONEBOOK_GRACE nicht erreichbar, gelten
// die Sitzungen nicht mehr (fail-closed), bis eine Prüfung wieder gelingt.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	ModePhonebook           = "phonebook"
	pbDefaultTranscriptDays = 14 // ADR-011: Standard-Aufbewahrung der Transkripte im Modus phonebook
	pbProvider              = "phonebook"
	pbDefaultRevalidate     = 5 * time.Minute
	pbDefaultGrace          = 15 * time.Minute
	pbFreshForKeyReset      = 15 * time.Minute // Schlüssel zurücksetzen nur kurz nach der Anmeldung
	pbMaxValidateBatch      = 500
	pbKeyUCNGToPhonebook    = "ucng->phonebook"
	pbKeyPhonebookToUCNG    = "phonebook->ucng"
)

// PhonebookConfig: Anbindung an das Telefonbuch (nur MODE=phonebook).
type PhonebookConfig struct {
	Enabled     bool
	URL         string        // öffentliche Adresse des Telefonbuchs (Browser)
	InternalURL string        // Adresse für Server-zu-Server-Aufrufe (Standard: URL)
	SecretFile  string        // gemeinsames Geheimnis (mindestens 32 Zeichen)
	Revalidate  time.Duration // Abstand der Nachprüfung
	Grace       time.Duration // so lange gelten Sitzungen ohne erfolgreiche Prüfung
	Lifecycle   time.Duration // Abstand des Status-Abgleichs aller verknüpften Konten (Austritt)
	// Tage nach dem Austritt bis zur Anonymisierung
	RetentionDays int
	secret        []byte
}

var (
	ErrPBCode     = errors.New("Die Anmeldung über das Telefonbuch ist abgelaufen oder ungültig – bitte erneut versuchen")
	ErrPBDown     = errors.New("Das Telefonbuch ist gerade nicht erreichbar – bitte später erneut versuchen")
	ErrPBClosed   = errors.New("Im Telefonbuch-Modus erfolgt die Anmeldung über das Telefonbuch")
	ErrPBClaims   = errors.New("Das Telefonbuch hat unvollständige Angaben geliefert")
	ErrPBNoAdmin  = errors.New("Für den Admin-Bereich fehlt die Zuordnung im Telefonbuch")
	ErrPBNotYet   = errors.New("Diese Person hat die Kommunikation noch nicht genutzt – eine Nachricht geht erst, wenn sie sich einmal angemeldet hat")
	ErrPBSelf     = errors.New("Das bist du selbst")
	ErrPBNoWidget = errors.New("Im Telefonbuch-Betrieb gibt es kein Widget für fremde Webseiten (kein Gastzugang)")
	ErrPBDMPlain  = errors.New("Direktnachrichten sind hier immer Ende-zu-Ende-verschlüsselt – die Verschlüsselung muss bei euch beiden eingerichtet sein")
)

// load prüft die Einstellungen und liest das Geheimnis. Fehler beenden den Start.
func (c *PhonebookConfig) load() error {
	if !c.Enabled {
		return nil
	}
	pub, err := pbBaseURL(c.URL, "PHONEBOOK_URL", false)
	if err != nil {
		return err
	}
	c.URL = pub
	if c.InternalURL == "" {
		c.InternalURL = c.URL
	}
	if c.InternalURL, err = pbBaseURL(c.InternalURL, "PHONEBOOK_INTERNAL_URL", true); err != nil {
		return err
	}
	if c.Revalidate <= 0 {
		c.Revalidate = pbDefaultRevalidate
	}
	if c.Grace <= 0 {
		c.Grace = pbDefaultGrace
	}
	if c.Revalidate < 30*time.Second || c.Revalidate > time.Hour {
		return errors.New("PHONEBOOK_REVALIDATE: 30s bis 1h")
	}
	if c.Grace < c.Revalidate || c.Grace > 24*time.Hour {
		return errors.New("PHONEBOOK_GRACE: mindestens PHONEBOOK_REVALIDATE, höchstens 24h")
	}
	if c.Lifecycle <= 0 {
		c.Lifecycle = pbDefaultLifecycle
	}
	if c.Lifecycle < 10*time.Minute || c.Lifecycle > 7*24*time.Hour {
		return errors.New("PHONEBOOK_LIFECYCLE: 10m bis 7 Tage")
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = pbDefaultRetention
	}
	if c.RetentionDays < 1 || c.RetentionDays > 3650 {
		return errors.New("PHONEBOOK_RETENTION_DAYS: 1 bis 3650 Tage")
	}
	if c.SecretFile == "" {
		return errors.New("PHONEBOOK_SECRET_FILE fehlt (gemeinsames Geheimnis mit dem Telefonbuch)")
	}
	fi, err := os.Stat(c.SecretFile)
	if err != nil {
		return fmt.Errorf("PHONEBOOK_SECRET_FILE: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return errors.New("PHONEBOOK_SECRET_FILE ist keine Datei")
	}
	if fi.Mode().Perm()&0o004 != 0 {
		return errors.New("PHONEBOOK_SECRET_FILE ist für alle lesbar – bitte chmod 440 o. ä.")
	}
	b, err := os.ReadFile(c.SecretFile)
	if err != nil {
		return fmt.Errorf("PHONEBOOK_SECRET_FILE: %w", err)
	}
	b = bytes.TrimSpace(b)
	if len(b) < 32 {
		return errors.New("PHONEBOOK_SECRET_FILE: das Geheimnis braucht mindestens 32 Zeichen")
	}
	c.secret = b
	return nil
}

// pbBaseURL: nur Herkunft (Schema, Host, Port), HTTPS außer für localhost. Die interne Adresse
// (Server-zu-Server) darf zusätzlich http mit einem Namen ohne Punkt nutzen – das ist ein
// Dienstname im selben Docker-Netz (z. B. http://app:8080), der Verkehr verlässt den Host nicht.
// IP-Adressen und Namen mit Punkt brauchen HTTPS (ADR-027).
func pbBaseURL(raw, name string, internal bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", fmt.Errorf("%s: vollständige Adresse nötig, z. B. https://telefonbuch.firma.local", name)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s: nur Schema und Host, ohne Pfad", name)
	}
	host := u.Hostname()
	sameHost := internal && host != "" && !strings.ContainsAny(host, ".:") // IPv4 hat Punkte, IPv6 Doppelpunkte
	if u.Scheme == "http" && host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasSuffix(host, ".localhost") && !sameHost {
		if internal {
			return "", fmt.Errorf("%s: HTTPS ist Pflicht (http nur für localhost oder einen Dienstnamen im Docker-Netz, z. B. http://app:8080)", name)
		}
		return "", fmt.Errorf("%s: HTTPS ist Pflicht (http nur für localhost)", name)
	}
	return u.Scheme + "://" + u.Host, nil
}

// key leitet je Richtung einen eigenen Schlüssel ab – eine Anfrage in die eine Richtung lässt
// sich nicht in die andere spiegeln.
func (c PhonebookConfig) key(dir string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("ucng-phonebook-v1|" + dir))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) phonebook() bool { return s.cfg.Phonebook.Enabled }

// pbClaims: Identität aus dem Telefonbuch (Einlösen und Nachprüfen).
type pbClaims struct {
	Subject        string    `json:"subject"`  // stabile principal_id des Telefonbuchs
	Username       string    `json:"username"` // Anmeldename (kann sich ändern)
	DisplayName    string    `json:"displayName"`
	Role           string    `json:"role"`    // "", moderator, admin (ADR-008)
	Session        string    `json:"session"` // Bezug zur Telefonbuch-Sitzung
	SessionExpires time.Time `json:"sessionExpires"`
	Groups         []string  `json:"groups,omitempty"` // org:<id> (Einheit und Oberheiten), lead:<id> (Leitung/Vertretung)
	Avatar         string    `json:"avatar,omitempty"` // PH-06: Prüfsumme des Startbilds aus dem Telefonbuch
}

var pbSubjectRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func (c *pbClaims) valid() bool {
	if !pbSubjectRe.MatchString(c.Subject) || !pbSubjectRe.MatchString(c.Session) || c.Username == "" || len(c.Username) > 64 {
		return false
	}
	switch c.Role {
	case "", RoleModerator, RoleAdmin:
	default:
		return false
	}
	if len(c.Groups) > 500 {
		return false
	}
	for _, g := range c.Groups {
		if !pbGroupRe.MatchString(g) {
			return false
		}
	}
	if len(c.DisplayName) > 200 {
		c.DisplayName = c.DisplayName[:200]
	}
	return !c.SessionExpires.IsZero()
}

type pbCheck struct {
	Session string `json:"session"`
	Subject string `json:"subject"`
	Active  bool   `json:"active"` // in UCNG kürzlich benutzt (zählt im Telefonbuch als Aktivität)
}

type pbResult struct {
	Session string    `json:"session"`
	Valid   bool      `json:"valid"`
	Reason  string    `json:"reason,omitempty"` // logout, expired, disabled, forbidden
	Claims  *pbClaims `json:"claims,omitempty"`
}

// pbClient spricht mit dem Telefonbuch (Server zu Server).
type pbClient struct {
	cfg  PhonebookConfig
	http *http.Client

	mu       sync.Mutex
	lastOK   time.Time
	lastErr  string
	failures int
}

func newPBClient(cfg PhonebookConfig) *pbClient {
	return &pbClient{cfg: cfg, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type pbHTTPError struct{ status int }

func (e pbHTTPError) Error() string { return fmt.Sprintf("Telefonbuch antwortet HTTP %d", e.status) }

func (p *pbClient) post(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.InternalURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.key(pbKeyUCNGToPhonebook))
	res, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return pbHTTPError{res.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

func (p *pbClient) note(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		if p.failures > 0 {
			log.Printf("Telefonbuch wieder erreichbar")
		}
		p.lastOK, p.lastErr, p.failures = time.Now(), "", 0
		return
	}
	p.failures++
	if p.failures == 1 || p.lastErr != err.Error() {
		log.Printf("Telefonbuch: Nachprüfung fehlgeschlagen: %v", err)
	}
	p.lastErr = err.Error()
}

// redeem löst einen Code ein. 4xx = Code abgelehnt (abgelaufen, benutzt, falscher verifier,
// Konto gesperrt); alles andere = Telefonbuch nicht erreichbar.
func (p *pbClient) redeem(ctx context.Context, code, verifier string) (pbClaims, error) {
	var c pbClaims
	err := p.post(ctx, "/api/internal/communication/redeem", map[string]string{"code": code, "verifier": verifier}, &c)
	var he pbHTTPError
	switch {
	case errors.As(err, &he) && he.status >= 400 && he.status < 500:
		return c, ErrPBCode
	case err != nil:
		log.Printf("Telefonbuch: Einlösen fehlgeschlagen: %v", err)
		return c, ErrPBDown
	case !c.valid():
		return c, ErrPBClaims
	}
	return c, nil
}

func (p *pbClient) validate(ctx context.Context, items []pbCheck) ([]pbResult, error) {
	var out struct {
		Results []pbResult `json:"results"`
	}
	if err := p.post(ctx, "/api/internal/communication/validate", map[string]any{"sessions": items}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// revalidate prüft alle Sitzungen aus dem Telefonbuch einmal nach.
func (s *Server) pbRevalidate(ctx context.Context) error {
	items := s.users.ExtSessions(s.cfg.Phonebook.Revalidate)
	for len(items) > 0 {
		n := min(len(items), pbMaxValidateBatch)
		res, err := s.pb.validate(ctx, items[:n])
		if err != nil {
			s.pb.note(err)
			return err
		}
		s.users.ExtApply(items[:n], res)
		items = items[n:]
	}
	s.pb.note(nil)
	s.pbSyncAvatars(ctx) // PH-06: Startbilder aus dem Telefonbuch nachziehen
	return nil
}

func (s *Server) pbLoop() {
	t := time.NewTicker(s.cfg.Phonebook.Revalidate)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = s.pbRevalidate(ctx)
			cancel()
			s.users.ExtSweep()
		}
	}
}

// pbPeer: Anfrage vom Telefonbuch? (Bearer mit abgeleitetem Schlüssel, konstante Laufzeit)
func (s *Server) pbPeer(r *http.Request) bool {
	want := s.cfg.Phonebook.key(pbKeyPhonebookToUCNG)
	got := bearer(r)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// phonebookOnly: Endpunkte, die es nur im Modus phonebook gibt.
func (s *Server) phonebookOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.phonebook() {
			writeErr(w, http.StatusNotFound, "Nur im Modus phonebook (MODE=phonebook)")
			return
		}
		next(w, r)
	}
}

// pbStaleGate (PH-04, gefunden im Ende-zu-Ende-Test): Ist das Telefonbuch länger als die Frist
// nicht erreichbar, gelten Sitzungen nicht (fail-closed). Bisher kam dann 401 – die Oberfläche
// hielt das für „abgemeldet“, verwarf das Token und die Sitzung kam nach dem Ausfall nicht
// zurück. Jetzt: 503 mit Hinweis; Zugriff bleibt gesperrt, das Token bleibt gültig.
func (s *Server) pbStaleGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s2s := strings.HasPrefix(r.URL.Path, "/api/integration/") && r.URL.Path != "/api/integration/phonebook/user"
		if strings.HasPrefix(r.URL.Path, "/api/") && !s2s && s.users.ExtStale(bearer(r)) {
			w.Header().Set("Retry-After", "30")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": ErrPBDown.Error(), "phonebookDown": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// notPhonebook schließt Konto-Endpunkte, die im Modus phonebook nicht gelten
// (Passwort-Anmeldung, Registrierung, Einladungen, E-Mail, Zurücksetzen, 2FA).
func (s *Server) notPhonebook(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.phonebook() {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": ErrPBClosed.Error(), "phonebook": true})
			return
		}
		next(w, r)
	}
}

// pbDMPlain: Im Modus phonebook gibt es Direktnachrichten nur verschlüsselt (ADR-007: E2EE
// wird gegenüber hosted nicht abgeschwächt, auch wenn das Schlüsselpaar erst später entsteht).
func (s *Server) pbDMPlain(room string, me Identity) bool {
	if !s.phonebook() || !isDMName(room) {
		return false
	}
	enc, _, _ := s.e2eMode(room, me)
	return !enc
}

// PublicPhonebook steht in /api/settings.
type PublicPhonebook struct {
	Authorize string   `json:"authorize"` // Anmeldeseite des Telefonbuchs
	Home      string   `json:"home"`      // Startseite des Telefonbuchs
	Locks     []string `json:"locks"`     // aktive Betriebssperren (ADR-020, vom Telefonbuch geprüft)
	// ADR-011: Aufbewahrung der Transkripte und ob der Chat-Server selbst verschriftlicht (STT_URL)
	TranscriptKeepDays int  `json:"transcriptKeepDays"`
	STT                bool `json:"stt"`
}

func (s *Server) publicPhonebook() *PublicPhonebook {
	if !s.phonebook() {
		return nil
	}
	return &PublicPhonebook{Authorize: s.cfg.Phonebook.URL + "/api/addons/communication/authorize", Home: s.cfg.Phonebook.URL + "/", Locks: s.cfg.Locks.Names(),
		TranscriptKeepDays: s.cfg.TranscriptKeepDays, STT: s.cfg.STTURL != ""}
}

var pbVerifierRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var pbCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,128}$`)

// POST /api/integration/phonebook/exchange {"code","verifier"} → Sitzung (wie /api/account/login)
func (s *Server) handlePBExchange(w http.ResponseWriter, r *http.Request) {
	if !s.acctLimit(w, r) {
		return
	}
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if !pbCodeRe.MatchString(in.Code) || !pbVerifierRe.MatchString(in.Verifier) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": ErrPBCode.Error(), "retry": true})
		return
	}
	c, err := s.pb.redeem(r.Context(), in.Code, in.Verifier)
	switch {
	case errors.Is(err, ErrPBCode), errors.Is(err, ErrPBClaims):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "retry": true})
		return
	case err != nil:
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	res, created, err := s.users.ExtLogin(c, r.UserAgent())
	if err != nil {
		acctFail(w, err)
		return
	}
	if created {
		log.Printf("Telefonbuch: Konto für %q angelegt (%s)", c.Username, res.ID)
	}
	if res.Renamed { // Anzeigename im Telefonbuch geändert: bisherige Nachrichten nachziehen
		s.hub.RenameUser(res.ID, res.Name)
	}
	go s.pbSyncAvatarsBG() // PH-06: Startbild gleich übernehmen, nicht erst bei der nächsten Nachprüfung
	s.writeLogin(w, r, res, map[string]any{"created": created})
}

// POST /api/integration/phonebook/revoke {"sessions":[…],"subjects":[…]} – vom Telefonbuch
func (s *Server) handlePBRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.pbPeer(r) {
		if !s.acctLimit(w, r) {
			return
		}
		writeErr(w, http.StatusUnauthorized, "Nicht berechtigt")
		return
	}
	var in struct {
		Sessions []string `json:"sessions"`
		Subjects []string `json:"subjects"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Sessions)+len(in.Subjects) > 10000 {
		writeErr(w, http.StatusRequestEntityTooLarge, "Zu viele Einträge")
		return
	}
	n := s.users.ExtRevoke(in.Sessions, in.Subjects)
	writeJSON(w, http.StatusOK, map[string]any{"ended": n})
}

// POST /api/integration/phonebook/admin – Sitzung für den Admin-Bereich aus der Chat-Sitzung
// (nur mit Zuordnung admin/moderator im Telefonbuch, ADR-008). Die Admin-Sitzung endet mit der
// Chat-Sitzung und sobald die Zuordnung wegfällt.
func (s *Server) handlePBAdmin(w http.ResponseWriter, r *http.Request) {
	me, ok := s.users.Lookup(bearer(r))
	if !ok || !me.Member {
		writeErr(w, http.StatusUnauthorized, "Bitte zuerst über das Telefonbuch anmelden")
		return
	}
	ext, ok := s.users.ExtOf(me.ID, me.Session)
	if !ok || (ext.Role != RoleAdmin && ext.Role != RoleModerator) {
		writeErr(w, http.StatusForbidden, ErrPBNoAdmin.Error())
		return
	}
	actor := Actor{Name: "pb:" + ext.Username, Display: me.Name, Role: ext.Role}
	id, sid, role := me.ID, me.Session, ext.Role
	tok := s.accounts.LoginBound(actor, func() bool {
		e, ok := s.users.ExtOf(id, sid)
		return ok && (e.Role == role || e.Role == RoleAdmin)
	})
	s.log(r.WithContext(context.WithValue(r.Context(), actorCtxKey{}, actor)), AuditEntry{Action: "admin.sso", Detail: ext.Role})
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "role": actor.Role, "name": actor.label()})
}
