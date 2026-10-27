package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kommunikations-Add-on, PH-03: Organisation und Rechte.
//
//   - Jede Person bekommt in den Identitätsdaten ihre Gruppen: org:<id> für die eigene Einheit
//     und alle Oberheiten (Kanal einer Einheit = Einheit + Untereinheiten, Entscheidung
//     26.10.2026) sowie lead:<id> für Einheiten, die sie leitet oder vertritt (fest eingetragen
//     oder über das Leitungs-Schlagwort der Ebene – wie in der Organisationsansicht).
//   - Den Organisationsbaum schickt das Telefonbuch als Gesamtstand an UCNG (PUT …/org),
//     versioniert über einen Hash: bei jeder Änderung und spätestens alle 30 min. Damit braucht
//     es für Organisationsänderungen keine Ereignis-Warteschlange (ADR-018).
//   - UCNG fragt den Status aller verknüpften Personen ab (POST …/principals) – Grundlage für
//     „sperren, nach Frist anonymisieren“ beim Austritt.

const (
	commOrgEvery   = time.Minute
	commOrgRefresh = 30 * time.Minute
)

// orgView: Organisation einmal geladen (je Anfrage bzw. Abgleich).
type orgView struct {
	units  map[int64]OrgUnit
	levels []OrgLevel
}

func (a *App) loadOrg(ctx context.Context) (*orgView, error) {
	list, err := a.st().ListOrgUnits(ctx)
	if err != nil {
		return nil, err
	}
	o := &orgView{units: make(map[int64]OrgUnit, len(list)), levels: a.orgLevels(ctx)}
	for _, u := range list {
		o.units[u.ID] = u
	}
	return o, nil
}

// chain: Einheit und alle Oberheiten (von unten nach oben), schützt vor Zyklen.
func (o *orgView) chain(id int64) []OrgUnit {
	var out []OrgUnit
	seen := map[int64]bool{}
	for id != 0 && !seen[id] {
		u, ok := o.units[id]
		if !ok {
			break
		}
		seen[id] = true
		out = append(out, u)
		id = u.Parent
	}
	return out
}

func (o *orgView) depth(id int64) int { return len(o.chain(id)) }

func (o *orgView) level(id int64) OrgLevel {
	if d := o.depth(id); d > 0 && d <= len(o.levels) {
		return o.levels[d-1]
	}
	return OrgLevel{}
}

func (o *orgView) path(id int64) string {
	c := o.chain(id)
	parts := make([]string, 0, len(c))
	for i := len(c) - 1; i >= 0; i-- {
		parts = append(parts, unitLabelGo(c[i]))
	}
	return strings.Join(parts, " › ")
}

func unitLabelGo(u OrgUnit) string {
	if u.Short != "" {
		return u.Short + " – " + u.Name
	}
	return u.Name
}

func hasID(list []int64, id int64) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// groupsFor: Gruppen einer Person (nur aktive, nicht ausgeblendete Einträge).
func (o *orgView) groupsFor(e *EmployeeRec) []string {
	if e == nil || !e.Active || e.Hidden {
		return nil
	}
	var out []string
	add := func(g string) {
		if !contains(out, g) {
			out = append(out, g)
		}
	}
	for _, u := range o.chain(e.OrgUnit) {
		add("org:" + strconv.FormatInt(u.ID, 10))
	}
	for _, u := range o.units {
		if hasID(u.Leaders, e.ID) || hasID(u.Deputies, e.ID) {
			add("lead:" + strconv.FormatInt(u.ID, 10))
		}
	}
	if e.OrgUnit != 0 {
		if lv := o.level(e.OrgUnit); (lv.LeaderTag != 0 && hasID(e.Tags, lv.LeaderTag)) || (lv.DeputyTag != 0 && hasID(e.Tags, lv.DeputyTag)) {
			add("lead:" + strconv.FormatInt(e.OrgUnit, 10))
		}
	}
	sort.Strings(out)
	return out
}

// principalGroups: Gruppen zum Principal über die Verknüpfung Konto ↔ Eintrag (0 = keine).
func (a *App) principalGroups(ctx context.Context, o *orgView, p *Principal) ([]string, error) {
	id, err := a.st().EmployeeIDByUsername(ctx, p.Username)
	if err != nil || id == 0 {
		return nil, err
	}
	e, err := a.st().GetEmployee(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return o.groupsFor(e), nil
}

// ---------- Organisationsstand an UCNG ----------

type commUnit struct {
	ID     int64  `json:"id"`
	Parent int64  `json:"parent,omitempty"`
	Name   string `json:"name"`
	Short  string `json:"short,omitempty"`
	Path   string `json:"path,omitempty"`
	Room   bool   `json:"room"`
}

// orgPayload: Gesamtstand und Version (Hash) – gleiche Organisation ergibt gleiche Version.
func (a *App) orgPayload(ctx context.Context) ([]commUnit, string, error) {
	o, err := a.loadOrg(ctx)
	if err != nil {
		return nil, "", err
	}
	pol := a.commCfg()
	units := make([]commUnit, 0, len(o.units))
	for _, u := range o.units {
		room := pol.AutoCreateOrgRooms
		if room && len(pol.OrgRoomLevels) > 0 {
			room = false
			d := o.depth(u.ID)
			for _, l := range pol.OrgRoomLevels {
				room = room || l == d
			}
		}
		units = append(units, commUnit{ID: u.ID, Parent: u.Parent, Name: u.Name, Short: u.Short, Path: o.path(u.ID), Room: room})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	b, _ := json.Marshal(units)
	h := sha256.Sum256(b)
	return units, hex.EncodeToString(h[:8]), nil
}

type commOrgState struct {
	mu      sync.Mutex
	version string
	at      time.Time
	err     string
	result  json.RawMessage
}

func (a *App) pushOrg(ctx context.Context, force bool) error {
	units, version, err := a.orgPayload(ctx)
	if err != nil {
		return err
	}
	a.commOrg.mu.Lock()
	same := a.commOrg.version == version && a.commOrg.err == "" && time.Since(a.commOrg.at) < commOrgRefresh
	a.commOrg.mu.Unlock()
	if same && !force {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"version": version, "units": units})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPut, a.cfg.Communication.InternalBaseURL+"/api/integration/phonebook/org", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Communication.key(commKeyToUCNG))
	res, err := commHTTPClient().Do(req)
	var out bytes.Buffer
	if err == nil {
		defer res.Body.Close()
		_, _ = out.ReadFrom(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode != http.StatusOK {
			err = fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(out.String()))
		}
	}
	a.commOrg.mu.Lock()
	defer a.commOrg.mu.Unlock()
	a.commOrg.at = time.Now()
	if err != nil {
		if a.commOrg.err != err.Error() {
			log.Printf("Kommunikation: Organisationsabgleich fehlgeschlagen: %v", err)
		}
		a.commOrg.err = err.Error()
		return err
	}
	if a.commOrg.version != version {
		log.Printf("Kommunikation: Organisation abgeglichen (Stand %s, %d Einheiten)", version, len(units))
	}
	a.commOrg.version, a.commOrg.err, a.commOrg.result = version, "", json.RawMessage(out.Bytes())
	return nil
}

func (a *App) commOrgLoop(ctx context.Context) {
	t := time.NewTicker(commOrgEvery)
	defer t.Stop()
	for {
		if snap := a.comm.snapshot(); snap.Status == "ok" { // nur an ein geprüftes UCNG
			_ = a.pushOrg(ctx, false)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------- Status für den Austritt ----------

// POST /api/internal/communication/principals {"subjects":[…]} → Status je Principal
func (a *App) handleCommPrincipals(w http.ResponseWriter, r *http.Request) {
	if !a.commPeer(w, r) {
		return
	}
	var in struct {
		Subjects []string `json:"subjects"`
	}
	if !decodeJSON(w, r, &in, 1<<20) {
		return
	}
	if len(in.Subjects) > commMaxValidate {
		jsonError(w, http.StatusRequestEntityTooLarge, "Zu viele auf einmal")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	pol := a.commCfg()
	type res struct {
		Subject string `json:"subject"`
		Status  string `json:"status"`
	}
	out := make([]res, 0, len(in.Subjects))
	for _, s := range in.Subjects {
		p, err := a.st().GetPrincipal(ctx, s)
		switch {
		case errors.Is(err, ErrNotFound):
			out = append(out, res{s, "unknown"})
			continue
		case err != nil: // Datenbank weg: keine Antwort statt falscher Sperren
			jsonError(w, http.StatusServiceUnavailable, "Datenbank nicht erreichbar")
			return
		}
		a.recheckPrincipal(ctx, p, now)
		switch {
		case p.Disabled:
			out = append(out, res{s, "disabled"})
		case !pol.allowed(p):
			out = append(out, res{s, "forbidden"})
		default:
			out = append(out, res{s, "active"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}
