package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kommunikations-Add-on, PH-02: gemeinsame Anmeldung (SSO) mit UCNG im Modus „phonebook“.
//
//   - GET  /api/addons/communication/authorize  (Browser, Telefonbuch-Sitzung) → einmaliger Code,
//     Weiterleitung zu UCNG mit dem Code im Fragment. Authorization Code + PKCE (S256) + state
//     (ADR-005). Der Code gilt 60 s, nur einmal und nur zusammen mit dem verifier des Browsers.
//   - POST /api/internal/communication/redeem   (UCNG, gemeinsames Geheimnis) → Identität
//   - POST /api/internal/communication/validate (UCNG, regelmäßig) → gilt die Sitzung noch?
//   - Abmeldung im Telefonbuch → POST <UCNG>/api/integration/phonebook/revoke (mit Wiederholung)
//
// Das Geheimnis wird je Richtung abgeleitet (HMAC) – eine Anfrage von UCNG lässt sich nicht als
// Anfrage an UCNG wiederverwenden. Die Codes liegen nur im Speicher (eine Instanz, wie die
// Anmeldesperren); nach einem Neustart sind höchstens 60 s alte Codes verloren.

const (
	commCodeTTL      = 60 * time.Second
	commMaxCodes     = 10000
	commKeyFromUCNG  = "ucng->phonebook"
	commKeyToUCNG    = "phonebook->ucng"
	commRoleAdmin    = "admin"
	commRoleMod      = "moderator"
	commMaxValidate  = 1000
	commRevokeTries  = 8
	commDefaultCheck = 15 * time.Minute
	commDefaultGrace = time.Hour
)

// CommAccess: Wer darf die Kommunikation nutzen? Leer = alle angemeldeten Personen (außer DEV-Admin).
type CommAccess struct {
	Users      []string `json:"users"`      // Benutzernamen
	Groups     []string `json:"groups"`     // LDAP-Gruppen (DN)
	AllowLocal *bool    `json:"allowLocal"` // lokale Konten (Standard: ja)
}

// CommRoles: Zuordnung zu Admin/Moderator in UCNG (ADR-008: ausdrücklich, nicht automatisch).
type CommRoles struct {
	AdminUsers      []string `json:"adminUsers"`
	AdminGroups     []string `json:"adminGroups"`
	ModeratorUsers  []string `json:"moderatorUsers"`
	ModeratorGroups []string `json:"moderatorGroups"`
	// Nur zur Inbetriebnahme: Telefonbuch-Admins werden UCNG-Admins. Danach ausschalten.
	PhonebookAdminsAreUCNGAdmins bool `json:"phonebookAdminsAreUCNGAdmins"`
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hasAny(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

func contains(list []string, s string) bool { return hasAny([]string{s}, list) }

// validateSSO prüft den PH-02-Teil der Konfiguration (wird von validate aufgerufen).
func (c *CommunicationConfig) validateSSO(ldapOn bool) error {
	c.Access.Users, c.Access.Groups = lowerAll(c.Access.Users), lowerAll(c.Access.Groups)
	r := &c.Roles
	r.AdminUsers, r.AdminGroups, r.ModeratorUsers, r.ModeratorGroups = lowerAll(r.AdminUsers), lowerAll(r.AdminGroups), lowerAll(r.ModeratorUsers), lowerAll(r.ModeratorGroups)
	if !ldapOn && (len(c.Access.Groups)+len(r.AdminGroups)+len(r.ModeratorGroups) > 0) {
		return errors.New("communication: Gruppen (access.groups, roles.*Groups) brauchen LDAP")
	}
	if c.ADRecheck.Duration == 0 {
		c.ADRecheck = Duration{commDefaultCheck}
	}
	if c.ADGrace.Duration == 0 {
		c.ADGrace = Duration{commDefaultGrace}
	}
	if c.ADRecheck.Duration < time.Minute || c.ADRecheck.Duration > 24*time.Hour {
		return errors.New("communication.adRecheck: 1m bis 24h")
	}
	if c.ADGrace.Duration < c.ADRecheck.Duration || c.ADGrace.Duration > 7*24*time.Hour {
		return errors.New("communication.adGrace: mindestens adRecheck, höchstens 7 Tage")
	}
	for _, l := range c.OrgRoomLevels {
		if l < 1 || l > 10 {
			return errors.New("communication.orgRoomLevels: 1–10")
		}
	}
	if r.PhonebookAdminsAreUCNGAdmins && !c.quiet {
		log.Printf("WARNUNG: communication.roles.phonebookAdminsAreUCNGAdmins ist an – nur zur Inbetriebnahme gedacht. Danach Admins ausdrücklich zuordnen (adminUsers/adminGroups) und ausschalten (ADR-008).")
	}
	return nil
}

// ldapGroups: alle Gruppen, deren Mitgliedschaft das LDAP festhalten soll
func (c *CommunicationConfig) ldapGroups() []string {
	var out []string
	for _, g := range [][]string{c.Access.Groups, c.Roles.AdminGroups, c.Roles.ModeratorGroups} {
		for _, x := range g {
			if !contains(out, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// roleFor: Rolle in UCNG (ADR-008)
func (c *CommunicationConfig) roleFor(p *Principal) string {
	u := strings.ToLower(p.Username)
	r := c.Roles
	switch {
	case contains(r.AdminUsers, u), hasAny(p.Groups, r.AdminGroups), r.PhonebookAdminsAreUCNGAdmins && p.IsAdmin:
		return commRoleAdmin
	case contains(r.ModeratorUsers, u), hasAny(p.Groups, r.ModeratorGroups):
		return commRoleMod
	}
	return ""
}

// allowed: Nutzungsrecht (communication.use)
func (c *CommunicationConfig) allowed(p *Principal) bool {
	if p == nil || p.Disabled {
		return false
	}
	if p.Provider == "local" && c.Access.AllowLocal != nil && !*c.Access.AllowLocal {
		return false
	}
	if c.roleFor(p) != "" {
		return true
	}
	if len(c.Access.Users) == 0 && len(c.Access.Groups) == 0 {
		return true
	}
	return contains(c.Access.Users, strings.ToLower(p.Username)) || hasAny(p.Groups, c.Access.Groups)
}

func (c *CommunicationConfig) key(dir string) string {
	m := hmac.New(sha256.New, []byte(c.secret))
	m.Write([]byte("ucng-phonebook-v1|" + dir))
	return hex.EncodeToString(m.Sum(nil))
}

// ---------- Principal bei der Anmeldung ----------

// principalForLogin legt den Principal einer Anmeldung an bzw. aktualisiert ihn. Fehler verhindern
// die Anmeldung am Telefonbuch nicht – die Kommunikation ist dann für diese Sitzung nicht nutzbar.
func (a *App) principalForLogin(ctx context.Context, user, display, source string, isAdmin bool, lu *LDAPUser) string {
	if !a.cfg.Communication.Enabled || source == "dev" {
		return ""
	}
	p := &Principal{Provider: source, Subject: user, Username: user, DisplayName: display, IsAdmin: isAdmin, CheckedAt: time.Now().UTC()}
	if lu != nil {
		p.Provider, p.Groups = "ldap", lu.Groups
		if lu.GUID != "" {
			p.Subject = strings.ToLower(lu.GUID)
		} else {
			p.Subject = "name:" + user
		}
	}
	if err := a.st().UpsertPrincipal(ctx, p); err != nil {
		log.Printf("Kommunikation: Principal für %q: %v", user, err)
		return ""
	}
	return p.ID
}

// ---------- Codes ----------

type commCode struct {
	principal string
	session   string // IDHash der Telefonbuch-Sitzung
	challenge string
	exp       time.Time
}

type commCodes struct {
	mu sync.Mutex
	m  map[string]commCode // sha256(code) → Eintrag
}

func (c *commCodes) issue(e commCode, now time.Time) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]commCode{}
	}
	for k, v := range c.m {
		if now.After(v.exp) {
			delete(c.m, k)
		}
	}
	if len(c.m) >= commMaxCodes {
		return "", errors.New("zu viele offene Anmeldungen")
	}
	code := randomToken(32)
	e.exp = now.Add(commCodeTTL)
	c.m[sha256hex(code)] = e
	return code, nil
}

// take: einmalig – der Code ist danach weg, auch wenn die weitere Prüfung scheitert.
func (c *commCodes) take(code string, now time.Time) (commCode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := sha256hex(code)
	e, ok := c.m[k]
	delete(c.m, k)
	return e, ok && !now.After(e.exp)
}

var reCommState = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var reCommChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var reCommVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func s256(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// GET /api/addons/communication/authorize?state=…&challenge=… (Navigation im Browser)
func (a *App) handleCommAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	cfg := &a.cfg.Communication
	q := r.URL.Query()
	state, challenge := q.Get("state"), q.Get("challenge")
	if !reCommState.MatchString(state) || !reCommChallenge.MatchString(challenge) || q.Get("method") != "S256" {
		http.Error(w, "Ungültige Anmeldeanfrage der Kommunikation", http.StatusBadRequest)
		return
	}
	back := func(params url.Values) {
		params.Set("pb_state", state)
		http.Redirect(w, r, cfg.PublicBaseURL+"/#"+params.Encode(), http.StatusSeeOther)
	}
	fail := func(reason string) { back(url.Values{"pb_error": {reason}}) }
	s, err := a.loadSession(r)
	if err != nil {
		log.Printf("Kommunikation authorize: %v", err)
		fail("unavailable")
		return
	}
	resume := url.Values{"commState": {state}, "commChallenge": {challenge}}
	if s != nil && s.PrincipalID == "" && s.Source != "dev" && (q.Get("relogin") == "1" || a.offline.Load()) {
		// gerade erst neu angemeldet und trotzdem kein Principal (z. B. Datenbank nur lesend):
		// nicht im Kreis schicken
		fail("unavailable")
		return
	}
	if s == nil || (s.PrincipalID == "" && s.Source != "dev") {
		// nicht angemeldet bzw. Sitzung von vor dem Einschalten des Add-ons: neu anmelden, dann weiter
		if s != nil {
			_ = a.st().DeleteSession(r.Context(), s.IDHash)
		}
		http.Redirect(w, r, "/?"+resume.Encode(), http.StatusSeeOther)
		return
	}
	if s.Source == "dev" {
		fail("forbidden")
		return
	}
	p, err := a.st().GetPrincipal(r.Context(), s.PrincipalID)
	if err != nil || !a.commCfg().allowed(p) {
		a.audit(r, s.Username, "communication_denied", "", "")
		fail("forbidden")
		return
	}
	if snap := a.comm.snapshot(); snap.Status != "ok" { // fail-closed: keine Anmeldung an einem unsicheren UCNG
		if snap.Status == "insecure" {
			fail("insecure")
		} else {
			fail("unavailable")
		}
		return
	}
	code, err := a.commCodes.issue(commCode{principal: p.ID, session: s.IDHash, challenge: challenge}, time.Now())
	if err != nil {
		fail("unavailable")
		return
	}
	back(url.Values{"pb_code": {code}})
}

// ---------- Server zu Server ----------

// commPeer: Anfrage von UCNG (abgeleiteter Schlüssel, konstante Laufzeit); Fehlversuche bremsen.
func (a *App) commPeer(w http.ResponseWriter, r *http.Request) bool {
	ip := a.clientIP(r)
	if blocked, _ := a.limiter.blocked(ip, ""); blocked {
		jsonError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche")
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.Communication.key(commKeyFromUCNG))) != 1 {
		a.limiter.fail(ip, "")
		log.Printf("Kommunikation: Server-Anfrage mit falschem Schlüssel von %s", ip)
		jsonError(w, http.StatusUnauthorized, "Nicht berechtigt")
		return false
	}
	return true
}

type commClaims struct {
	Subject        string    `json:"subject"`
	Username       string    `json:"username"`
	DisplayName    string    `json:"displayName"`
	Role           string    `json:"role"`
	Session        string    `json:"session"`
	SessionExpires time.Time `json:"sessionExpires"`
	Groups         []string  `json:"groups,omitempty"` // PH-03: org:<id>, lead:<id> (commorg.go)
	Avatar         string    `json:"avatar,omitempty"` // PH-06: Prüfsumme des Startbilds (commavatar.go)
}

// claimsFor: Identität für UCNG. o (Organisation) wird je Anfrage einmal geladen.
func (a *App) claimsFor(ctx context.Context, o *orgView, p *Principal, rec *SessionRec) (commClaims, error) {
	groups, err := a.principalGroups(ctx, o, p)
	if err != nil {
		return commClaims{}, err
	}
	return commClaims{Subject: p.ID, Username: p.Username, DisplayName: p.DisplayName, Role: a.commCfg().roleFor(p),
		Session: rec.IDHash, SessionExpires: rec.ExpiresAt, Groups: groups, Avatar: a.commAvatarHash(ctx, p.ID)}, nil
}

// liveSession: gilt die Telefonbuch-Sitzung noch (wie loadSession, aber über den Hash)?
func (a *App) liveSession(ctx context.Context, hash string, now time.Time) (*SessionRec, string, error) {
	rec, err := a.st().GetSession(ctx, hash)
	if err != nil {
		return nil, "", err
	}
	if rec == nil {
		return nil, "logout", nil
	}
	if now.After(rec.ExpiresAt) || now.Sub(rec.LastSeen) > a.cfg.Auth.SessionIdle.Duration {
		_ = a.st().DeleteSession(ctx, hash)
		return nil, "expired", nil
	}
	return rec, "", nil
}

// POST /api/internal/communication/redeem {"code","verifier"}
func (a *App) handleCommRedeem(w http.ResponseWriter, r *http.Request) {
	if !a.commPeer(w, r) {
		return
	}
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	if !decodeJSON(w, r, &in, 4<<10) {
		return
	}
	now := time.Now().UTC()
	c, ok := a.commCodes.take(in.Code, now)
	if !ok || !reCommVerifier.MatchString(in.Verifier) || subtle.ConstantTimeCompare([]byte(s256(in.Verifier)), []byte(c.challenge)) != 1 {
		jsonError(w, http.StatusBadRequest, "Code ungültig, abgelaufen oder bereits benutzt")
		return
	}
	rec, why, err := a.liveSession(r.Context(), c.session, now)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
		return
	}
	if rec == nil || rec.PrincipalID != c.principal {
		jsonError(w, http.StatusBadRequest, "Telefonbuch-Sitzung beendet ("+why+")")
		return
	}
	p, err := a.st().GetPrincipal(r.Context(), c.principal)
	if err != nil || !a.commCfg().allowed(p) {
		jsonError(w, http.StatusForbidden, "Kein Zugang zur Kommunikation")
		return
	}
	o, err := a.loadOrg(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
		return
	}
	cl, err := a.claimsFor(r.Context(), o, p, rec)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
		return
	}
	a.audit(r, p.Username, "communication_login", "", cl.Role)
	writeJSON(w, http.StatusOK, cl)
}

type commCheck struct {
	Session string `json:"session"`
	Subject string `json:"subject"`
	Active  bool   `json:"active"`
}

type commResult struct {
	Session string      `json:"session"`
	Valid   bool        `json:"valid"`
	Reason  string      `json:"reason,omitempty"`
	Claims  *commClaims `json:"claims,omitempty"`
}

// POST /api/internal/communication/validate {"sessions":[{session,subject,active}]}
func (a *App) handleCommValidate(w http.ResponseWriter, r *http.Request) {
	if !a.commPeer(w, r) {
		return
	}
	var in struct {
		Sessions []commCheck `json:"sessions"`
	}
	if !decodeJSON(w, r, &in, 1<<20) {
		return
	}
	if len(in.Sessions) > commMaxValidate {
		jsonError(w, http.StatusRequestEntityTooLarge, "Zu viele Sitzungen auf einmal")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	principals := map[string]*Principal{}
	pol := a.commCfg()
	o, err := a.loadOrg(ctx)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
		return
	}
	out := make([]commResult, 0, len(in.Sessions))
	for _, c := range in.Sessions {
		res := commResult{Session: c.Session}
		rec, why, err := a.liveSession(ctx, c.Session, now)
		if err != nil { // Datenbank weg: nicht beantworten → UCNG lässt die Frist laufen
			jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
			return
		}
		if rec == nil || rec.PrincipalID == "" || rec.PrincipalID != c.Subject {
			if why == "" {
				why = "logout"
			}
			res.Reason = why
			out = append(out, res)
			continue
		}
		p, seen := principals[rec.PrincipalID]
		if !seen {
			if p, err = a.st().GetPrincipal(ctx, rec.PrincipalID); err == nil {
				a.recheckPrincipal(ctx, p, now)
			} else if !errors.Is(err, ErrNotFound) {
				jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
				return
			}
			principals[rec.PrincipalID] = p
		}
		switch {
		case p == nil || p.Disabled:
			res.Reason = "disabled"
		case !pol.allowed(p):
			res.Reason = "forbidden"
		default:
			cl, err := a.claimsFor(ctx, o, p, rec)
			if err != nil {
				jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
				return
			}
			res.Valid = true
			res.Claims = &cl
			if c.Active && now.Sub(rec.LastSeen) > time.Minute { // Nutzung der Kommunikation hält die Sitzung wach
				_ = a.st().TouchSession(ctx, rec.IDHash, now)
			}
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// recheckPrincipal: AD-Status nachprüfen (ADR-013), höchstens alle adRecheck. Ist das Verzeichnis
// nicht erreichbar, gilt der letzte Stand bis adGrace – danach fail-closed (Disabled).
func (a *App) recheckPrincipal(ctx context.Context, p *Principal, now time.Time) {
	cfg := &a.cfg.Communication
	if now.Sub(p.CheckedAt) < cfg.ADRecheck.Duration {
		return
	}
	was := *p
	switch p.Provider {
	case "local":
		lu, err := a.st().GetLocalUser(ctx, p.Username)
		switch {
		case errors.Is(err, ErrNotFound):
			p.Disabled = true
		case err != nil:
			return
		default:
			p.Disabled, p.IsAdmin, p.DisplayName = lu.Disabled, lu.IsAdmin, lu.DisplayName
		}
		p.CheckedAt = now
	case "ldap":
		if a.ldap == nil {
			return
		}
		lu, err := a.ldap.LookupUser(p.Username)
		switch {
		case errors.Is(err, errLDAPUserGone), errors.Is(err, errNotAllowed):
			p.Disabled = true
			p.CheckedAt = now
		case err != nil:
			if now.Sub(p.CheckedAt) > cfg.ADGrace.Duration {
				if !p.Disabled {
					log.Printf("Kommunikation: AD-Status von %q seit %v nicht prüfbar (%v) – gesperrt bis zur nächsten erfolgreichen Prüfung", p.Username, now.Sub(p.CheckedAt).Round(time.Minute), err)
				}
				p.Disabled = true // CheckedAt bleibt: die nächste erfolgreiche Prüfung hebt das wieder auf
				break
			}
			return
		default:
			if g := strings.ToLower(lu.GUID); g != "" && !strings.HasPrefix(p.Subject, "name:") && g != p.Subject {
				p.Disabled = true // gleicher Name, andere Person (Konto neu angelegt)
			} else {
				p.Disabled, p.IsAdmin, p.Groups = lu.Disabled, lu.IsAdmin, lu.Groups
				if lu.DisplayName != "" {
					p.DisplayName = lu.DisplayName
				}
			}
			p.CheckedAt = now
		}
	default:
		return
	}
	if err := a.st().UpsertPrincipal(ctx, p); err != nil {
		log.Printf("Kommunikation: Principal %s speichern: %v", p.ID, err)
	}
	if p.Disabled && !was.Disabled {
		log.Printf("Kommunikation: %q ist im Verzeichnis gesperrt bzw. ohne Zugang – alle Sitzungen werden beendet", p.Username)
		_ = a.st().DeleteUserSessions(ctx, p.Username, p.Provider)
		a.commRevoke(nil, []string{p.ID})
		a.audit(nil, "system", "communication_revoke", p.Username, "AD-Nachprüfung")
	}
}

// ---------- Widerruf (Outbox) ----------

type commRevokeReq struct {
	Sessions []string `json:"sessions,omitempty"`
	Subjects []string `json:"subjects,omitempty"`
}

// commRevoke meldet beendete Sitzungen bzw. gesperrte Personen an UCNG – im Hintergrund mit
// Wiederholung. Geht das nicht durch, beendet spätestens die nächste Nachprüfung durch UCNG die
// Sitzung (dort fragt UCNG ohnehin regelmäßig nach).
func (a *App) commRevoke(sessions, subjects []string) {
	if !a.cfg.Communication.Enabled || a.commOut == nil || (len(sessions) == 0 && len(subjects) == 0) {
		return
	}
	select {
	case a.commOut <- commRevokeReq{Sessions: sessions, Subjects: subjects}:
	default:
		log.Printf("Kommunikation: Widerruf-Warteschlange voll – UCNG beendet die Sitzung bei der nächsten Nachprüfung")
	}
}

func (a *App) commRevokeLoop(ctx context.Context) {
	client := commHTTPClient()
	send := func(req commRevokeReq) error {
		b, _ := json.Marshal(req)
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		hr, err := http.NewRequestWithContext(cctx, http.MethodPost, a.cfg.Communication.InternalBaseURL+"/api/integration/phonebook/revoke", bytes.NewReader(b))
		if err != nil {
			return err
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Authorization", "Bearer "+a.cfg.Communication.key(commKeyToUCNG))
		res, err := client.Do(hr)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-a.commOut:
			delay := time.Second
			for try := 1; ; try++ {
				err := send(req)
				if err == nil {
					break
				}
				if try >= commRevokeTries {
					log.Printf("Kommunikation: Widerruf nicht zustellbar (%v) – UCNG beendet die Sitzung bei der nächsten Nachprüfung", err)
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				delay = min(delay*2, time.Minute)
			}
		}
	}
}

// commMe: Angaben zur Kommunikation für /api/me (Menüpunkt). nil = Add-on aus.
func (a *App) commMe(ctx context.Context, s *Session) map[string]any {
	cfg := &a.cfg.Communication
	if !cfg.Enabled || s == nil {
		return nil
	}
	out := map[string]any{"url": cfg.PublicBaseURL, "allowed": false}
	if s.Source == "dev" {
		out["reason"] = "Der DEV-Admin hat keinen Zugang zur Kommunikation"
		return out
	}
	if s.PrincipalID == "" {
		out["allowed"] = true // Principal entsteht beim nächsten Anmelden (authorize schickt dorthin)
		return out
	}
	p, err := a.st().GetPrincipal(ctx, s.PrincipalID)
	pol := a.commCfg()
	out["allowed"] = err == nil && pol.allowed(p)
	if av := a.commAvatarMe(ctx, s); av != nil && out["allowed"] == true {
		out["avatar"] = av // PH-06: Startbild in der Kommunikation aktuell halten
	}
	if err == nil && !pol.allowed(p) {
		out["reason"] = "Kein Zugang zur Kommunikation"
	}
	return out
}
