package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Kommunikations-Add-on, PH-03: Verwaltung im Telefonbuch (Menü „Verwaltung → Kommunikation“).
//
// Nutzungsrecht, Admin-/Moderatorzuordnung (ADR-008) und Organisationskanäle stehen zunächst in
// der Konfigurationsdatei (Block „communication“). Ändert ein Admin sie in der Oberfläche, gilt
// ab dann der gespeicherte Stand (Einstellung „communicationPolicy“, protokolliert). „Auf
// Konfigurationsdatei zurücksetzen“ entfernt ihn wieder. Verbindung, Geheimnis und Adressen
// bleiben ausschließlich in der Konfigurationsdatei.

const settingCommPolicy = "communicationPolicy"

// CommPolicy: der in der Oberfläche änderbare Teil.
type CommPolicy struct {
	Access             CommAccess `json:"access"`
	Roles              CommRoles  `json:"roles"`
	AutoCreateOrgRooms bool       `json:"autoCreateOrgRooms"`
	OrgRoomLevels      []int      `json:"orgRoomLevels"`
}

type commPolicyCache struct {
	mu     sync.Mutex
	cfg    *CommunicationConfig
	source string
	at     time.Time
}

func (c *CommunicationConfig) policy() CommPolicy {
	return CommPolicy{Access: c.Access, Roles: c.Roles, AutoCreateOrgRooms: c.AutoCreateOrgRooms, OrgRoomLevels: c.OrgRoomLevels}
}

func (c *CommunicationConfig) apply(p CommPolicy) {
	c.Access, c.Roles, c.AutoCreateOrgRooms, c.OrgRoomLevels = p.Access, p.Roles, p.AutoCreateOrgRooms, p.OrgRoomLevels
}

// commCfg: wirksame Einstellungen (Datei, ggf. durch die Oberfläche überschrieben), 30 s gepuffert.
func (a *App) commCfg() *CommunicationConfig {
	cfg, _ := a.commCfgSource()
	return cfg
}

func (a *App) commCfgSource() (*CommunicationConfig, string) {
	a.commPol.mu.Lock()
	defer a.commPol.mu.Unlock()
	if a.commPol.cfg != nil && time.Since(a.commPol.at) < 30*time.Second {
		return a.commPol.cfg, a.commPol.source
	}
	c := a.cfg.Communication // Kopie
	c.quiet = true
	source := "config"
	if v, err := a.st().GetSetting(context.Background(), settingCommPolicy); err == nil && v != "" {
		var p CommPolicy
		if json.Unmarshal([]byte(v), &p) == nil {
			c.apply(p)
			if c.validateSSO(a.cfg.ldapEnabled()) == nil {
				source = "admin"
			} else {
				c = a.cfg.Communication // ungültig gespeichert: Datei gilt (fail-safe)
			}
		}
	}
	a.commPol.cfg, a.commPol.source, a.commPol.at = &c, source, time.Now()
	return &c, source
}

func (a *App) commCfgInvalidate() {
	a.commPol.mu.Lock()
	a.commPol.cfg = nil
	a.commPol.mu.Unlock()
}

// GET /api/admin/communication
func (a *App) handleCommAdminGet(w http.ResponseWriter, r *http.Request) {
	cfg, source := a.commCfgSource()
	levels := a.orgLevels(r.Context())
	names := make([]string, len(levels))
	for i, l := range levels {
		names[i] = l.Name
	}
	a.commOrg.mu.Lock()
	org := map[string]any{"version": a.commOrg.version, "error": a.commOrg.err}
	if !a.commOrg.at.IsZero() {
		org["at"] = a.commOrg.at.UTC().Format(time.RFC3339)
	}
	if len(a.commOrg.result) > 0 {
		org["result"] = a.commOrg.result
	}
	a.commOrg.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": cfg.policy(), "source": source, "status": a.comm.snapshot(), "org": org,
		"ldap": a.cfg.ldapEnabled(), "levels": names, "url": cfg.PublicBaseURL,
	})
}

// PUT /api/admin/communication {policy}
func (a *App) handleCommAdminPut(w http.ResponseWriter, r *http.Request) {
	var p CommPolicy
	if !decodeJSON(w, r, &p, 256<<10) {
		return
	}
	for _, l := range p.OrgRoomLevels {
		if l < 1 || l > 10 {
			jsonError(w, http.StatusBadRequest, "Ebenen für Organisationskanäle: 1–10")
			return
		}
	}
	sort.Ints(p.OrgRoomLevels)
	if len(p.Access.Users)+len(p.Access.Groups)+len(p.Roles.AdminUsers)+len(p.Roles.AdminGroups)+len(p.Roles.ModeratorUsers)+len(p.Roles.ModeratorGroups) > 2000 {
		jsonError(w, http.StatusBadRequest, "Zu viele Einträge")
		return
	}
	c := a.cfg.Communication
	c.quiet = true
	c.apply(p)
	if err := c.validateSSO(a.cfg.ldapEnabled()); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p = c.policy() // normalisiert (klein geschrieben, ohne Leereinträge)
	b, _ := json.Marshal(p)
	if err := a.st().SetSetting(r.Context(), settingCommPolicy, string(b)); err != nil {
		dbFail(w, err)
		return
	}
	a.commCfgInvalidate()
	s := sessionFrom(r)
	a.audit(r, s.Username, "communication_policy", "", string(b))
	if p.Roles.PhonebookAdminsAreUCNGAdmins {
		log.Printf("WARNUNG: %s hat „Telefonbuch-Admins werden Admins der Kommunikation“ eingeschaltet – nur zur Inbetriebnahme (ADR-008)", s.Username)
	}
	go func() { // Organisationskanäle gleich nachziehen
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = a.pushOrg(ctx, true)
	}()
	a.handleCommAdminGet(w, r)
}

// DELETE /api/admin/communication – zurück zur Konfigurationsdatei
func (a *App) handleCommAdminReset(w http.ResponseWriter, r *http.Request) {
	if err := a.st().SetSetting(r.Context(), settingCommPolicy, ""); err != nil {
		dbFail(w, err)
		return
	}
	a.commCfgInvalidate()
	a.audit(r, sessionFrom(r).Username, "communication_policy", "", "Konfigurationsdatei")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = a.pushOrg(ctx, true)
	}()
	a.handleCommAdminGet(w, r)
}

// POST /api/admin/communication/sync – Organisation sofort abgleichen
func (a *App) handleCommAdminSync(w http.ResponseWriter, r *http.Request) {
	if snap := a.comm.snapshot(); snap.Status != "ok" {
		jsonError(w, http.StatusServiceUnavailable, "Kommunikation nicht einsatzbereit ("+snap.Status+")")
		return
	}
	if err := a.pushOrg(r.Context(), true); err != nil {
		msg := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "Zeitüberschreitung"
		}
		jsonError(w, http.StatusBadGateway, "Abgleich fehlgeschlagen: "+msg)
		return
	}
	a.audit(r, sessionFrom(r).Username, "communication_sync", "", "")
	a.handleCommAdminGet(w, r)
}
