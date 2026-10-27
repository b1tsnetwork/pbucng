// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Organisation aus dem Telefonbuch (Modus „phonebook“, PH-03).
//
// Das Telefonbuch liefert seinen Organisationsbaum als Gesamtstand (PUT …/org, idempotent,
// versioniert) und je Person die Gruppen, zu denen sie gehört:
//   - pb:org:<id>  – Mitglied der Einheit oder einer ihrer Untereinheiten
//   - pb:lead:<id> – Leitung oder Vertretung der Einheit
//
// Diese Gruppen wirken wie Rollen bei den Kanalrechten (readRoles/writeRoles) – Admins können
// sie also auch für eigene private Kanäle wählen – und als Moderationsrecht je Kanal (modRoles).
// Für Einheiten mit Kanal legt UCNG einen privaten Kanal an (Gruppe „Organisation“), hält Name,
// Beschreibung und Rechte aktuell und archiviert ihn, wenn die Einheit wegfällt: Dann sehen ihn
// nur noch Team und Admin-Bereich; gelöscht wird nichts.

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

const (
	pbGroupPrefix = "pb:"
	pbOrgRoomsGrp = "g_pborg"
	pbMaxUnits    = 5000
)

var pbGroupRe = regexp.MustCompile(`^(org|lead):[0-9]{1,18}$`)

// pbGroup: eine Gruppe aus dem Telefonbuch, wie sie im Admin-Bereich erscheint.
type pbGroup struct {
	ID    string `json:"id"` // pb:org:12
	Label string `json:"label"`
}

type pbCatalog struct {
	mu      sync.Mutex
	version string
	groups  map[string]pbGroup
}

type pbCatalogState struct {
	Version string    `json:"version"`
	Groups  []pbGroup `json:"groups"`
}

func newPBCatalog() *pbCatalog { return &pbCatalog{groups: map[string]pbGroup{}} }

func (c *pbCatalog) has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.groups[id]
	return ok
}

func (c *pbCatalog) list() []pbGroup {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]pbGroup, 0, len(c.groups))
	for _, g := range c.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Label < out[j].Label || out[i].Label == out[j].Label && out[i].ID < out[j].ID
	})
	return out
}

func (c *pbCatalog) state() pbCatalogState {
	l := c.list()
	c.mu.Lock()
	defer c.mu.Unlock()
	return pbCatalogState{Version: c.version, Groups: l}
}

func (c *pbCatalog) restore(st pbCatalogState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version, c.groups = st.Version, map[string]pbGroup{}
	for _, g := range st.Groups {
		if strings.HasPrefix(g.ID, pbGroupPrefix) && pbGroupRe.MatchString(strings.TrimPrefix(g.ID, pbGroupPrefix)) {
			c.groups[g.ID] = g
		}
	}
}

// filterRoles: Rollen-IDs und bekannte Telefonbuch-Gruppen (Reihenfolge: Rollen, dann Gruppen).
func (h *Hub) filterRoles(ids []string) []string {
	out := h.roles.Filter(ids)
	for _, id := range ids {
		if strings.HasPrefix(id, pbGroupPrefix) && h.pbCat.has(id) && !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// pbGroups wandelt die Gruppen aus den Claims in Zugriffsrollen um (unbekanntes Format fällt weg).
func pbGroups(in []string) []string {
	var out []string
	for _, g := range in {
		if pbGroupRe.MatchString(g) && !contains(out, pbGroupPrefix+g) {
			out = append(out, pbGroupPrefix+g)
		}
	}
	sort.Strings(out)
	return out
}

// --- Organisationskanäle ---

type pbUnit struct {
	ID     int64  `json:"id"`
	Parent int64  `json:"parent,omitempty"`
	Name   string `json:"name"`
	Short  string `json:"short,omitempty"`
	Path   string `json:"path,omitempty"` // „Dezernat II › Ordnungsamt“ (Beschreibung)
	Room   bool   `json:"room"`           // Kanal für diese Einheit
}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue")

// orgSlug: Kanalname aus dem Namen der Einheit (Kleinbuchstaben, Ziffern, -).
func orgSlug(u pbUnit) string {
	name := u.Name
	if u.Short != "" && len([]rune(u.Short)) <= 8 {
		name = u.Short + " " + u.Name
	}
	s := strings.ToLower(umlauts.Replace(name))
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := unicode.IsLower(r) || unicode.Is(unicode.Lo, r) || unicode.IsDigit(r)
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if rs := []rune(out); len(rs) > 32 {
		out = strings.TrimRight(string(rs[:32]), "-")
	}
	if !ValidRoom(out) {
		out = "einheit-" + strconv.FormatInt(u.ID, 10)
	}
	return out
}

// PBOrgResult: Ergebnis eines Abgleichs.
type PBOrgResult struct {
	Created  []string `json:"created"`
	Updated  []string `json:"updated"`
	Archived []string `json:"archived"`
}

var ErrPBOrg = errors.New("Ungültiger Organisationsstand")

// SyncPBOrg übernimmt den Organisationsstand des Telefonbuchs: Gruppenkatalog und Kanäle.
func (h *Hub) SyncPBOrg(version string, units []pbUnit) (PBOrgResult, error) {
	res := PBOrgResult{Created: []string{}, Updated: []string{}, Archived: []string{}}
	if len(units) > pbMaxUnits || len(version) > 80 {
		return res, ErrPBOrg
	}
	seen := map[int64]bool{}
	groups := map[string]pbGroup{}
	for i := range units {
		u := &units[i]
		name, ok := CleanName(u.Name)
		if !ok || u.ID <= 0 || seen[u.ID] {
			return res, ErrPBOrg
		}
		seen[u.ID] = true
		u.Name = name
		u.Short, _ = cleanText(u.Short, 20)
		label := u.Name
		if u.Short != "" {
			label = u.Short + " – " + u.Name
		}
		id := strconv.FormatInt(u.ID, 10)
		groups[pbGroupPrefix+"org:"+id] = pbGroup{ID: pbGroupPrefix + "org:" + id, Label: label}
		groups[pbGroupPrefix+"lead:"+id] = pbGroup{ID: pbGroupPrefix + "lead:" + id, Label: "Leitung: " + label}
	}
	// Katalog zuerst – sonst fielen die neuen Gruppen bei den Kanalrechten heraus.
	h.pbCat.mu.Lock()
	h.pbCat.version, h.pbCat.groups = version, groups
	h.pbCat.mu.Unlock()
	h.touchDoc(pbOrgDoc)
	h.pruneRoomRoles() // entfernte Einheiten: Kanäle nie plötzlich öffentlich (nur noch Team)

	// Bestehende Organisationskanäle
	h.mu.Lock()
	byUnit := map[int64]string{}
	for n, r := range h.rooms {
		if r.orgUnit != 0 {
			byUnit[r.orgUnit] = n
		}
	}
	h.mu.Unlock()
	want := map[int64]bool{}
	for _, u := range units {
		if !u.Room {
			continue
		}
		want[u.ID] = true
		id := strconv.FormatInt(u.ID, 10)
		read, mod := []string{pbGroupPrefix + "org:" + id}, []string{pbGroupPrefix + "lead:" + id}
		desc := u.Path
		if desc == "" {
			desc = u.Name
		}
		if rs := []rune(desc); len(rs) > 140 {
			desc = string(rs[:139]) + "…"
		}
		slug := orgSlug(u)
		if name, ok := byUnit[u.ID]; ok {
			h.mu.Lock()
			r := h.rooms[name]
			in := RoomInput{Name: &name, Description: desc, SlowMode: r.slowMode, Links: r.links, BotSlowMode: r.botSlow, BotLinks: r.botLinks,
				ReadRoles: read, WriteRoles: nil, Voice: r.voice}
			newName, rec := name, r.orgSlug
			if r.name == r.orgSlug && slug != r.orgSlug { // nur automatisch vergebene Namen nachziehen
				if _, taken := h.rooms[slug]; !taken {
					newName, rec = slug, slug
				}
			}
			changed := r.description != desc || newName != name || r.orgArchived || strings.Join(r.readRoles, ",") != read[0] ||
				len(r.writeRoles) > 0 || strings.Join(r.modRoles, ",") != mod[0]
			h.mu.Unlock()
			if !changed {
				continue
			}
			in.Name = &newName
			if _, err := h.UpdateRoomWith(name, in); err != nil {
				return res, fmt.Errorf("Kanal %s: %w", name, err)
			}
			h.setOrgMeta(newName, u.ID, rec, mod, false)
			res.Updated = append(res.Updated, newName)
			continue
		}
		name := slug
		h.mu.Lock()
		if _, taken := h.rooms[name]; taken {
			name = strings.TrimRight(string([]rune(slug)[:min(len([]rune(slug)), 32-len(id)-1)]), "-") + "-" + id
		}
		h.mu.Unlock()
		if _, err := h.CreateRoomWith(RoomInput{Name: &name, Description: desc, ReadRoles: read}); err != nil {
			return res, fmt.Errorf("Kanal %s: %w", name, err)
		}
		h.setOrgMeta(name, u.ID, slugIf(name == slug, slug, ""), mod, false)
		h.placeOrgRoom(name)
		res.Created = append(res.Created, name)
	}
	// Einheit weg oder ohne Kanal: archivieren (nur noch Team), nichts löschen
	for unit, name := range byUnit {
		if want[unit] {
			continue
		}
		h.mu.Lock()
		r := h.rooms[name]
		archived := r == nil || r.orgArchived
		h.mu.Unlock()
		if archived {
			continue
		}
		h.mu.Lock()
		r.readRoles, r.writeRoles, r.modRoles, r.orgArchived = []string{RoleTeamOnly}, []string{RoleTeamOnly}, nil, true
		h.kickUnreadableLocked(r)
		h.record(storeOp{kind: opRoomMeta, room: name})
		h.mu.Unlock()
		res.Archived = append(res.Archived, name)
	}
	if len(res.Created)+len(res.Updated)+len(res.Archived) > 0 {
		h.mu.Lock()
		h.broadcastAll(Event{Type: "layout"})
		h.mu.Unlock()
	}
	return res, nil
}

func slugIf(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

const pbOrgDoc = "phonebook-org.json"

func (h *Hub) setOrgMeta(name string, unit int64, slug string, mod []string, archived bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[name]; r != nil {
		r.orgUnit, r.orgSlug, r.modRoles, r.orgArchived = unit, slug, mod, archived
		h.record(storeOp{kind: opRoomMeta, room: name})
	}
}

// placeOrgRoom: neuer Organisationskanal in die Gruppe „Organisation“ (einmalig; danach
// bestimmt der Admin-Bereich die Anordnung).
func (h *Hub) placeOrgRoom(name string) {
	if !h.groups.has(pbOrgRoomsGrp) {
		h.groups.mu.Lock()
		if len(h.groups.list) < maxGroups {
			h.groups.list = append(h.groups.list, RoomGroup{ID: pbOrgRoomsGrp, Name: "Organisation"})
		}
		h.groups.mu.Unlock()
		h.touchDoc("groups.json")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[name]; r != nil {
		r.group, r.pos = pbOrgRoomsGrp, h.nextPosLocked()
		h.record(storeOp{kind: opRoomMeta, room: name})
	}
}

// RoomMods: Moderationsrecht im Kanal über modRoles (Leitung/Vertretung einer Einheit).
func (h *Hub) RoomMods(roomName string, roles []string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.rooms[roomName]
	return ok && len(r.modRoles) > 0 && intersects(r.modRoles, roles)
}

// permsIn: Rechte einer Person in einem Kanal – global aus den Rollen, dazu Löschen und
// Anpinnen für die Moderation des Kanals (modRoles).
func (s *Server) permsIn(me Identity, ip, room string) Perms {
	p := s.posterOf(me, ip).Perms
	if s.hub.RoomMods(room, me.Roles) {
		p.Delete, p.Pin = true, true
	}
	return p
}

// --- HTTP ---

// PUT /api/integration/phonebook/org {"version","units":[…]} – vom Telefonbuch
func (s *Server) handlePBOrg(w http.ResponseWriter, r *http.Request) {
	if !s.pbPeer(r) {
		if !s.acctLimit(w, r) {
			return
		}
		writeErr(w, http.StatusUnauthorized, "Nicht berechtigt")
		return
	}
	var in struct {
		Version string   `json:"version"`
		Units   []pbUnit `json:"units"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := s.hub.SyncPBOrg(in.Version, in.Units)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n := len(res.Created) + len(res.Updated) + len(res.Archived); n > 0 {
		s.hub.AuditLog().Add(AuditEntry{Actor: "system", Display: "Telefonbuch", Action: "phonebook.org",
			Detail: fmt.Sprintf("Organisation %s: %d neu, %d geändert, %d archiviert", in.Version, len(res.Created), len(res.Updated), len(res.Archived))})
	}
	writeJSON(w, http.StatusOK, res)
}

// GET /api/admin/phonebook/groups – Einheiten als Zielgruppen für Kanalrechte
func (s *Server) handleAdminPBGroups(w http.ResponseWriter, r *http.Request) {
	st := s.hub.pbCat.state()
	writeJSON(w, http.StatusOK, st)
}
