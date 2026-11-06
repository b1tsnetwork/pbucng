package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Organisations-Export für Fachverfahren (z. B. Antragswerk): Einheiten mit wirksamer Leitung,
// Personen mit Einheit und Vorgesetzter/m sowie die Räume – lesend, maschinenlesbar und nur mit
// Token (orgExport.token bzw. TB_ORG_EXPORT_TOKEN[_FILE]). Ohne Token ist der Endpunkt aus (404).
//
// Ausgeblendete und inaktive Einträge sind nicht enthalten. Gleiche Daten ergeben dieselbe
// Version (ETag), Abrufer können mit If-None-Match billig pollen.

const orgExportFormat = "telefonbuch-org/1"

type exportPerson struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username,omitempty"`
	FirstName   string   `json:"firstName"`
	LastName    string   `json:"lastName"`
	Name        string   `json:"name"`
	Email       string   `json:"email,omitempty"`
	Phone       string   `json:"phone,omitempty"`
	Mobile      string   `json:"mobile,omitempty"`
	Room        string   `json:"room,omitempty"`
	Amt         string   `json:"amt,omitempty"`
	Unit        int64    `json:"unit,omitempty"`
	UnitKey     string   `json:"unitKey,omitempty"`
	UnitName    string   `json:"unitName,omitempty"`
	UnitPath    string   `json:"unitPath,omitempty"`
	Function    string   `json:"function,omitempty"` // z. B. „Leitung Amt“ / „Stellvertretung Amt“
	Manager     string   `json:"manager,omitempty"`  // Anmeldename der/des Vorgesetzten
	ManagerName string   `json:"managerName,omitempty"`
	ManagerMail string   `json:"managerEmail,omitempty"`
	Deputies    []string `json:"deputies,omitempty"` // Anmeldenamen der Vertretungen (Reihenfolge zählt)
	Source      string   `json:"source,omitempty"`   // ad, manual
}

type exportRef struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
}

type exportUnit struct {
	ID        int64       `json:"id"`
	Key       string      `json:"key"` // Kürzel, sonst „OE-<id>“ – stabiler Schlüssel für Fremdsysteme
	Parent    int64       `json:"parent,omitempty"`
	ParentKey string      `json:"parentKey,omitempty"`
	Name      string      `json:"name"`
	Short     string      `json:"short,omitempty"`
	Label     string      `json:"label"` // „10 – Personal“
	Level     string      `json:"level,omitempty"`
	Depth     int         `json:"depth"`
	Path      string      `json:"path"`
	Top       string      `json:"top,omitempty"` // oberste Einheit (z. B. Dezernat)
	Sort      int         `json:"sort"`
	Leaders   []exportRef `json:"leaders"`
	Deputies  []exportRef `json:"deputies"`
	Head      *exportRef  `json:"head,omitempty"` // erste Leitung (auch geerbt, wenn die Einheit keine eigene hat)
	Email     string      `json:"email,omitempty"`
	Phone     string      `json:"phone,omitempty"`
	TicketURL string      `json:"ticketUrl,omitempty"`
	Members   int         `json:"members"`
}

type exportRoom struct {
	Name    string `json:"name"`
	Unit    int64  `json:"unit,omitempty"` // Einheit mit den meisten Personen im Raum
	UnitKey string `json:"unitKey,omitempty"`
	People  int    `json:"people"`
}

func (a *App) orgExportAllowed(r *http.Request) bool {
	tok := a.cfg.OrgExport.Token
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return tok != "" && ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(tok)) == 1
}

func (a *App) handleOrgExport(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OrgExport.Token == "" {
		http.NotFound(w, r)
		return
	}
	if !a.orgExportAllowed(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="org-export"`)
		http.Error(w, "nicht erlaubt", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	o, err := a.loadOrg(ctx)
	if err != nil {
		dbFail(w, err)
		return
	}
	recs, err := a.st().ListEmployees(ctx, false)
	if err != nil {
		dbFail(w, err)
		return
	}
	body := buildOrgExport(o, recs)
	levels := levelNames(o.levels)
	b, _ := json.Marshal([]any{levels, body.units, body.people}) // Räume folgen aus den Personen
	h := sha256.Sum256(b)
	version := hex.EncodeToString(h[:8])
	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	// personenbezogen: nie zwischenspeichern; Abrufer vergleichen selbst per If-None-Match
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// Nachvollziehbarkeit (Datenschutz): jeder vollständige Abruf, nicht die 304-Antworten
	a.audit(r, "org-export", "org_export", "", fmt.Sprintf("%d Einheiten, %d Personen, Version %s", len(body.units), len(body.people), version))
	writeJSON(w, http.StatusOK, map[string]any{
		"format": orgExportFormat, "version": version, "generated": time.Now().UTC().Format(time.RFC3339),
		"levels": levels, "units": body.units, "people": body.people, "rooms": body.rooms,
	})
}

func levelNames(lv []OrgLevel) []string {
	out := make([]string, 0, len(lv))
	for _, l := range lv {
		out = append(out, l.Name)
	}
	return out
}

type orgExport struct {
	units  []exportUnit
	people []exportPerson
	rooms  []exportRoom
}

func unitKey(u OrgUnit) string {
	if s := strings.TrimSpace(u.Short); s != "" {
		return s
	}
	return "OE-" + strconv.FormatInt(u.ID, 10)
}

// buildOrgExport: reine Funktion (testbar) – Leitung wie in der Organisationsansicht:
// fest eingetragen oder über das Leitungs-Schlagwort der Ebene.
func buildOrgExport(o *orgView, recs []EmployeeRec) orgExport {
	emp := map[int64]*EmployeeRec{}
	for i := range recs {
		if e := &recs[i]; e.Active && !e.Hidden {
			emp[e.ID] = e
		}
	}
	ref := func(id int64) (exportRef, bool) {
		e := emp[id]
		if e == nil {
			return exportRef{}, false
		}
		return exportRef{ID: e.ID, Username: e.Username, Name: strings.TrimSpace(e.FirstName + " " + e.LastName), Email: e.Email, Phone: e.Phone}, true
	}
	ids := make([]int64, 0, len(o.units))
	for id := range o.units {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// wirksame Leitung/Vertretung je Einheit
	leaders := map[int64][]int64{}
	deputies := map[int64][]int64{}
	add := func(m map[int64][]int64, unit, id int64) {
		if emp[id] != nil && !hasID(m[unit], id) {
			m[unit] = append(m[unit], id)
		}
	}
	for _, id := range ids {
		u := o.units[id]
		for _, l := range u.Leaders {
			add(leaders, id, l)
		}
		for _, d := range u.Deputies {
			add(deputies, id, d)
		}
	}
	members := map[int64]int{}
	for _, rid := range sortedEmpIDs(emp) {
		e := emp[rid]
		if e.OrgUnit == 0 {
			continue
		}
		members[e.OrgUnit]++
		lv := o.level(e.OrgUnit)
		if lv.LeaderTag != 0 && hasID(e.Tags, lv.LeaderTag) {
			add(leaders, e.OrgUnit, e.ID)
		}
		if lv.DeputyTag != 0 && hasID(e.Tags, lv.DeputyTag) {
			add(deputies, e.OrgUnit, e.ID)
		}
	}
	// erste Leitung der Einheit, sonst der nächsten Oberheit; skip: diese Person überspringen
	headOf := func(unit, skip int64) int64 {
		for _, u := range o.chain(unit) {
			for _, l := range leaders[u.ID] {
				if l != skip {
					return l
				}
			}
		}
		return 0
	}

	var out orgExport
	for _, id := range ids {
		u := o.units[id]
		c := o.chain(id)
		x := exportUnit{ID: id, Key: unitKey(u), Parent: u.Parent, Name: u.Name, Short: u.Short, Label: unitLabelGo(u),
			Level: o.level(id).Name, Depth: len(c), Path: o.path(id), Sort: u.Sort, Members: members[id],
			Leaders: []exportRef{}, Deputies: []exportRef{}}
		if p, ok := o.units[u.Parent]; ok {
			x.ParentKey = unitKey(p)
		}
		if len(c) > 0 {
			x.Top = unitLabelGo(c[len(c)-1])
		}
		for _, l := range leaders[id] {
			if r, ok := ref(l); ok {
				x.Leaders = append(x.Leaders, r)
			}
		}
		for _, d := range deputies[id] {
			if r, ok := ref(d); ok {
				x.Deputies = append(x.Deputies, r)
			}
		}
		if h, ok := ref(headOf(id, 0)); ok {
			x.Head = &h
		}
		// Kontaktweg: eigener oder geerbter
		for _, cu := range c {
			if cu.Contact != nil {
				x.Email, x.Phone, x.TicketURL = cu.Contact.Email, cu.Contact.Phone, cu.Contact.TicketURL
				break
			}
		}
		out.units = append(out.units, x)
	}

	type roomAgg struct {
		people int
		units  map[int64]int
	}
	rooms := map[string]*roomAgg{}
	for _, rid := range sortedEmpIDs(emp) {
		e := emp[rid]
		p := exportPerson{ID: e.ID, Username: e.Username, FirstName: e.FirstName, LastName: e.LastName,
			Name: strings.TrimSpace(e.FirstName + " " + e.LastName), Email: e.Email, Phone: e.Phone, Mobile: e.Mobile,
			Room: e.Room, Amt: e.Amt, Source: e.Source}
		if u, ok := o.units[e.OrgUnit]; ok {
			p.Unit, p.UnitKey, p.UnitName, p.UnitPath = u.ID, unitKey(u), u.Name, o.path(u.ID)
			lvName := o.level(u.ID).Name
			switch {
			case hasID(leaders[u.ID], e.ID):
				p.Function = strings.TrimSpace("Leitung " + lvName)
			case hasID(deputies[u.ID], e.ID):
				p.Function = strings.TrimSpace("Stellvertretung " + lvName)
			}
			if m := emp[headOf(u.ID, e.ID)]; m != nil {
				p.Manager, p.ManagerName, p.ManagerMail = m.Username, strings.TrimSpace(m.FirstName+" "+m.LastName), m.Email
			}
		}
		for _, d := range e.Deputies {
			if de := emp[d]; de != nil && de.Username != "" {
				p.Deputies = append(p.Deputies, de.Username)
			}
		}
		if room := strings.TrimSpace(e.Room); room != "" {
			ra := rooms[room]
			if ra == nil {
				ra = &roomAgg{units: map[int64]int{}}
				rooms[room] = ra
			}
			ra.people++
			if e.OrgUnit != 0 {
				ra.units[e.OrgUnit]++
			}
		}
		out.people = append(out.people, p)
	}
	names := make([]string, 0, len(rooms))
	for n := range rooms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ra := rooms[n]
		x := exportRoom{Name: n, People: ra.people}
		best := 0
		for uid, c := range ra.units {
			if c > best || (c == best && uid < x.Unit) {
				best, x.Unit = c, uid
			}
		}
		if u, ok := o.units[x.Unit]; ok {
			x.UnitKey = unitKey(u)
		}
		out.rooms = append(out.rooms, x)
	}
	if out.units == nil {
		out.units = []exportUnit{}
	}
	if out.people == nil {
		out.people = []exportPerson{}
	}
	if out.rooms == nil {
		out.rooms = []exportRoom{}
	}
	return out
}

func sortedEmpIDs(m map[int64]*EmployeeRec) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
