package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image/png"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// E-Mail-Signatur nach Styleguide (Korrespondenzschrift, Logo, Adressblock).
//
// Grunddaten (Organisation, Vorwahl, Postanschrift, Links, Logo, Schrift, Dienstgebäude, welche
// Ebene des Organigramms als „Amt“ bzw. „Sachgebiet“ gilt, Dienstgebäude je Einheit) kommen aus
// – der Verwaltung im Telefonbuch (Setting "signature", Logo in "signatureLogo"), sonst
// – einer eigenen Datei außerhalb des Projekts (signature.file bzw. TB_SIGNATURE_FILE, mit logoFile),
//   Beispiel: docs/signature.example.json, sonst
// – neutralen Vorgaben ohne Organisation und Logo (Generator dann aus).
// Personenbezogenes kommt aus dem eigenen Telefonbucheintrag und dem Organigramm; jede Person kann
// Angaben nur für ihre Signatur ergänzen oder abweichend setzen (persönliche Einstellungen).
// Das HTML entsteht hier im Server – Vorschau, Kopieren und Klartext haben damit eine Quelle.

const (
	settingSignature     = "signature"
	settingSignatureLogo = "signatureLogo" // PNG, Base64
	sigLogoMaxBytes      = 40 << 10        // passt Base64-kodiert in eine Einstellung (TEXT, 64 KB)
	sigSiteFree          = "frei"
)

type SigLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// SigSite: Dienstgebäude; Extra (Gebäude, Etage) steht in der Signatur über der Straße
type SigSite struct {
	ID     string `json:"id"`
	Group  string `json:"group"`
	Extra  string `json:"extra,omitempty"`
	Street string `json:"street"`
	Zip    string `json:"zip"`
	City   string `json:"city"`
}

type SigConfig struct {
	Enabled     bool              `json:"enabled"`
	Org         string            `json:"org"`         // z. B. „Stadt Musterstadt“ – fett über der Adresse
	AreaCode    string            `json:"areaCode"`    // Ortsvorwahl, z. B. 01234
	Exchange    string            `json:"exchange"`    // Anlagen-Rufnummer vor der Durchwahl, z. B. 56 → 56-1104
	MailDomain  string            `json:"mailDomain"`  // für Hinweis und Vorschlag aus dem Namen
	PostAddress string            `json:"postAddress"` // letzte Adresszeile
	Links       []SigLink         `json:"links"`
	Font        string            `json:"font"`
	FontSize    string            `json:"fontSize"`
	LineHeight  string            `json:"lineHeight"`
	Color       string            `json:"color"`
	LogoWidth   int               `json:"logoWidth"`  // Anzeigegröße in px (Datei doppelt so groß)
	LogoHeight  int               `json:"logoHeight"` //
	LogoAlt     string            `json:"logoAlt"`
	AmtLevel    int               `json:"amtLevel"` // Ebene im Organigramm (1 = oberste), 0 = nicht anzeigen
	SGLevel     int               `json:"sgLevel"`  // Ebene für „Sachgebiet“, 0 = nicht anzeigen
	Sites       []SigSite         `json:"sites"`
	DefaultSite string            `json:"defaultSite"`
	UnitSites   map[string]string `json:"unitSites"` // Einheit (ID oder Kürzel) → Dienstgebäude; gilt auch für Untereinheiten
	Stand       string            `json:"stand"`     // z. B. „Stand Oktober 2026“
	Note        string            `json:"note"`      // Hinweis über dem Formular
}

// defaultSigConfig: neutrale Vorgaben – keine Organisation, keine Adressen, kein Logo.
// Die eigenen Grunddaten gehören in eine Datei (signature.file) oder in die Verwaltung.
func defaultSigConfig() SigConfig {
	return SigConfig{
		Font: "Arial,Helvetica,sans-serif", FontSize: "11pt", LineHeight: "14pt", Color: "#1D1D1B",
		LogoWidth: 155, LogoHeight: 45, AmtLevel: 2, SGLevel: 3, UnitSites: map[string]string{},
	}
}

// sigFile: Grunddaten aus der Datei (beim Start geladen und geprüft)
type sigFile struct {
	path string
	cfg  SigConfig
	logo []byte
}

// loadSigFile liest die Datei; logoFile ist relativ zur Datei. Unbekannte Felder sind ein Fehler,
// damit Tippfehler nicht still verloren gehen.
func loadSigFile(path string) (*sigFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signature.file: %w", err)
	}
	var in struct {
		SigConfig
		LogoFile string `json:"logoFile"`
	}
	in.SigConfig = defaultSigConfig()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("signature.file %s: %w", path, err)
	}
	f := &sigFile{path: path, cfg: in.SigConfig}
	if in.LogoFile != "" {
		lp := in.LogoFile
		if !filepath.IsAbs(lp) {
			lp = filepath.Join(filepath.Dir(path), lp)
		}
		b, err := os.ReadFile(lp)
		if err != nil {
			return nil, fmt.Errorf("signature.file: logoFile: %w", err)
		}
		if f.logo, _, _, err = cleanSigLogo(b); err != nil {
			return nil, fmt.Errorf("signature.file: logoFile %s: %w", lp, err)
		}
	}
	return f, nil
}

// checkSigFile: Inhalt der Datei wie eine Eingabe in der Verwaltung prüfen (braucht das Organigramm)
func (a *App) checkSigFile(ctx context.Context) error {
	if a.sigFile == nil {
		return nil
	}
	c, err := a.cleanSigConfig(ctx, a.sigFile.cfg)
	if err != nil {
		return fmt.Errorf("signature.file %s: %w", a.sigFile.path, err)
	}
	if len(c.UnitSites) != len(a.sigFile.cfg.UnitSites) {
		log.Printf("Signatur: %d Zuordnungen in %s passen zu keiner Einheit/keinem Dienstgebäude und werden ignoriert",
			len(a.sigFile.cfg.UnitSites)-len(c.UnitSites), a.sigFile.path)
	}
	a.sigFile.cfg = c
	return nil
}

// sigSource: woher die wirksamen Grunddaten kommen ("admin", "file", "default")
func (a *App) sigSource(ctx context.Context) string {
	if v, err := a.st().GetSetting(ctx, settingSignature); err == nil && v != "" {
		return "admin"
	}
	if a.sigFile != nil {
		return "file"
	}
	return "default"
}

func (a *App) sigConfig(ctx context.Context) SigConfig {
	c := defaultSigConfig()
	if a.sigFile != nil {
		c = a.sigFile.cfg
		c.UnitSites = maps.Clone(c.UnitSites)
	}
	if v, err := a.st().GetSetting(ctx, settingSignature); err == nil && v != "" {
		var x SigConfig
		if json.Unmarshal([]byte(v), &x) == nil {
			c = x
		}
	}
	if c.UnitSites == nil {
		c.UnitSites = map[string]string{}
	}
	return c
}

func (a *App) sigLogo(ctx context.Context) (b []byte, custom bool) {
	if v, err := a.st().GetSetting(ctx, settingSignatureLogo); err == nil && v != "" {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) > 0 {
			return b, true
		}
	}
	if a.sigFile != nil {
		return a.sigFile.logo, false
	}
	return nil, false
}

// ---------- Prüfen der Grunddaten ----------

var (
	reSigColor  = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	reSigFont   = regexp.MustCompile(`^[A-Za-z0-9 ,'"-]{1,120}$`)
	reSigSize   = regexp.MustCompile(`^\d{1,2}(\.\d)?(pt|px)$`)
	reSigSiteID = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	reSigDigits = regexp.MustCompile(`^\d{0,8}$`)
)

func sigText(field, s string, max int, required bool) (string, error) {
	v, ok := cleanText(s, max)
	if !ok {
		return "", fmt.Errorf("%s: höchstens %d Zeichen, keine Steuerzeichen", field, max)
	}
	if required && v == "" {
		return "", fmt.Errorf("%s fehlt", field)
	}
	return v, nil
}

func sigURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	l := strings.ToLower(s)
	if len(s) > 300 || strings.ContainsAny(s, " \"'<>\\") {
		return "", false
	}
	return s, strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")
}

func (a *App) cleanSigConfig(ctx context.Context, in SigConfig) (SigConfig, error) {
	out := SigConfig{Enabled: in.Enabled, UnitSites: map[string]string{}}
	var err error
	t := func(dst *string, field, v string, max int, req bool) {
		if err == nil {
			*dst, err = sigText(field, v, max, req)
		}
	}
	t(&out.Org, "Organisation", in.Org, 120, true)
	t(&out.PostAddress, "Postanschrift", in.PostAddress, 160, false)
	t(&out.LogoAlt, "Alternativtext des Logos", in.LogoAlt, 120, false)
	t(&out.Stand, "Stand", in.Stand, 80, false)
	t(&out.Note, "Hinweis", in.Note, 500, false)
	t(&out.MailDomain, "E-Mail-Domain", strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.MailDomain), "@")), 100, false)
	if err != nil {
		return out, err
	}
	if out.MailDomain != "" && (strings.ContainsAny(out.MailDomain, " @/") || !strings.Contains(out.MailDomain, ".")) {
		return out, errors.New("E-Mail-Domain: z. B. musterstadt.de")
	}
	out.AreaCode, out.Exchange = strings.TrimSpace(in.AreaCode), strings.TrimSpace(in.Exchange)
	if !reSigDigits.MatchString(out.AreaCode) || (out.AreaCode != "" && !strings.HasPrefix(out.AreaCode, "0")) {
		return out, errors.New("Vorwahl: nur Ziffern, beginnt mit 0")
	}
	if !reSigDigits.MatchString(out.Exchange) {
		return out, errors.New("Anlagen-Rufnummer: nur Ziffern")
	}
	if len(in.Links) > 8 {
		return out, errors.New("Höchstens 8 Links")
	}
	for _, l := range in.Links {
		txt, e := sigText("Link-Text", l.Text, 120, false)
		if e != nil {
			return out, e
		}
		if txt == "" && strings.TrimSpace(l.URL) == "" {
			continue
		}
		u, ok := sigURL(l.URL)
		if !ok || txt == "" {
			return out, fmt.Errorf("Link %q: Text und Adresse mit https://, http:// oder mailto: angeben", txt)
		}
		out.Links = append(out.Links, SigLink{Text: txt, URL: u})
	}
	out.Font, out.FontSize, out.LineHeight, out.Color = strings.TrimSpace(in.Font), strings.TrimSpace(in.FontSize), strings.TrimSpace(in.LineHeight), strings.TrimSpace(in.Color)
	if !reSigFont.MatchString(out.Font) {
		return out, errors.New("Schrift: z. B. Arial,Helvetica,sans-serif")
	}
	if !reSigSize.MatchString(out.FontSize) || !reSigSize.MatchString(out.LineHeight) {
		return out, errors.New("Schriftgröße und Zeilenabstand: z. B. 11pt und 14pt")
	}
	if !reSigColor.MatchString(out.Color) {
		return out, errors.New("Schriftfarbe: z. B. #1D1D1B")
	}
	if in.LogoWidth < 20 || in.LogoWidth > 400 || in.LogoHeight < 10 || in.LogoHeight > 200 {
		return out, errors.New("Logo-Größe: Breite 20–400 px, Höhe 10–200 px")
	}
	out.LogoWidth, out.LogoHeight = in.LogoWidth, in.LogoHeight
	levels := len(a.orgLevels(ctx))
	if in.AmtLevel < 0 || in.AmtLevel > levels || in.SGLevel < 0 || in.SGLevel > levels {
		return out, fmt.Errorf("Ebenen: 0 (nicht anzeigen) bis %d", levels)
	}
	if in.SGLevel != 0 && in.SGLevel <= in.AmtLevel {
		return out, errors.New("Die Ebene für das Sachgebiet muss unter der Ebene für das Amt liegen")
	}
	out.AmtLevel, out.SGLevel = in.AmtLevel, in.SGLevel
	if len(in.Sites) > 300 {
		return out, errors.New("Höchstens 300 Dienstgebäude")
	}
	ids := map[string]bool{}
	for _, s := range in.Sites {
		var x SigSite
		x.ID = strings.TrimSpace(s.ID)
		if !reSigSiteID.MatchString(x.ID) || x.ID == sigSiteFree || ids[x.ID] {
			return out, fmt.Errorf("Dienstgebäude %q: ungültige oder doppelte Kennung", s.Street)
		}
		t(&x.Group, "Gruppe", s.Group, 80, false)
		t(&x.Extra, "Gebäude/Etage", s.Extra, 120, false)
		t(&x.Street, "Straße", s.Street, 120, true)
		t(&x.Zip, "PLZ", s.Zip, 10, true)
		t(&x.City, "Ort", s.City, 80, true)
		if err != nil {
			return out, fmt.Errorf("Dienstgebäude: %v", err)
		}
		ids[x.ID] = true
		out.Sites = append(out.Sites, x)
	}
	out.DefaultSite = in.DefaultSite
	if !ids[out.DefaultSite] {
		out.DefaultSite = ""
		if len(out.Sites) > 0 {
			out.DefaultSite = out.Sites[0].ID
		}
	}
	units, e := a.st().ListOrgUnits(ctx)
	if e != nil {
		return out, e
	}
	known := map[string]bool{}
	for _, u := range units {
		known[strconv.FormatInt(u.ID, 10)] = true
		if u.Short != "" {
			known[u.Short] = true // Kürzel: für Dateien, die in jeder Installation gelten sollen
		}
	}
	for k, v := range in.UnitSites {
		if known[k] && ids[v] {
			out.UnitSites[k] = v
		}
	}
	return out, nil
}

// ---------- Angaben einer Person ----------

// sigKeys: was eine Person für ihre Signatur setzen darf (Wert "" = bewusst leer)
var sigKeys = map[string]int{
	"firstName": 80, "lastName": 80, "position": 120, "phone": 40, "fax": 40, "mobile": 40, "email": 120, "room": 40,
	"amt": 20, "sg": 20, "site": 40, "freeExtra": 120, "freeStreet": 120, "freeZip": 10, "freeCity": 80,
}

func cleanSigInput(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		max, ok := sigKeys[k]
		if !ok {
			continue
		}
		c, ok := cleanText(v, max)
		if !ok {
			return nil, fmt.Errorf("%s: höchstens %d Zeichen", k, max)
		}
		if (k == "amt" || k == "sg") && c != "" {
			if _, err := strconv.ParseInt(c, 10, 64); err != nil {
				return nil, fmt.Errorf("%s: ungültige Einheit", k)
			}
		}
		out[k] = c
	}
	return out, nil
}

// sigData: alles, was in die Signatur kommt (nach Zusammenführen)
type sigData struct {
	FirstName, LastName, Position   string
	Phone, Fax, Mobile, Email, Room string
	Amt, SG                         string // Namen
	AmtID, SGID                     int64
	Site                            SigSite
}

type sigOrg struct {
	units  map[int64]OrgUnit
	depth  map[int64]int
	levels int
}

func newSigOrg(units []OrgUnit, levels int) sigOrg {
	o := sigOrg{units: map[int64]OrgUnit{}, depth: unitDepths(units), levels: levels}
	for _, u := range units {
		o.units[u.ID] = u
	}
	return o
}

// ancestorAt: Einheit auf der Ebene lv auf dem Weg von id zur Wurzel (0 = keine)
func (o sigOrg) ancestorAt(id int64, lv int) int64 {
	for n := 0; id != 0 && n < 64; n++ {
		if o.depth[id] == lv {
			return id
		}
		if o.depth[id] < lv {
			return 0
		}
		id = o.units[id].Parent
	}
	return 0
}

func (o sigOrg) within(id, root int64) bool {
	for n := 0; id != 0 && n < 64; n++ {
		if id == root {
			return true
		}
		id = o.units[id].Parent
	}
	return false
}

// siteFor: Dienstgebäude der Einheit oder der nächsten darüber, sonst Standard
func (c SigConfig) siteFor(o sigOrg, unit int64) string {
	for n := 0; unit != 0 && n < 64; n++ {
		if s, ok := c.UnitSites[strconv.FormatInt(unit, 10)]; ok {
			return s
		}
		unit = o.units[unit].Parent
	}
	return c.DefaultSite
}

func (c SigConfig) site(id string) (SigSite, bool) {
	for _, s := range c.Sites {
		if s.ID == id {
			return s, true
		}
	}
	return SigSite{}, false
}

// sigBase: Werte aus dem Telefonbucheintrag (ohne eigene Angaben)
type sigBase struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Phone     string `json:"phone"`
	Mobile    string `json:"mobile"`
	Email     string `json:"email"`
	Room      string `json:"room"`
	Amt       string `json:"amt"` // Einheit-ID als Text („“ = keine)
	SG        string `json:"sg"`
	Site      string `json:"site"`
}

func (a *App) sigBaseFor(ctx context.Context, s *Session, c SigConfig, o sigOrg, units []OrgUnit) (sigBase, bool) {
	var b sigBase
	b.Site = c.siteFor(o, 0)
	if s.EmployeeID == 0 {
		return b, false
	}
	e, err := a.st().GetEmployee(ctx, s.EmployeeID)
	if err != nil {
		return b, false
	}
	b.FirstName, b.LastName, b.Phone, b.Mobile, b.Email, b.Room = e.FirstName, e.LastName, e.Phone, e.Mobile, e.Email, e.Room
	unit := e.OrgUnit
	if _, ok := o.units[unit]; !ok {
		unit = newOrgMatcher(units).match(e.Amt).Unit // noch nicht zugeordnet: über den Amt-Text
	}
	if c.AmtLevel > 0 {
		if id := o.ancestorAt(unit, c.AmtLevel); id != 0 {
			b.Amt = strconv.FormatInt(id, 10)
		}
	}
	if c.SGLevel > 0 {
		if id := o.ancestorAt(unit, c.SGLevel); id != 0 {
			b.SG = strconv.FormatInt(id, 10)
		}
	}
	b.Site = c.siteFor(o, unit)
	return b, true
}

// merge: Werte aus dem Eintrag, überlagert von den eigenen Angaben
func (c SigConfig) merge(o sigOrg, b sigBase, in map[string]string) sigData {
	pick := func(k, def string) string {
		if v, ok := in[k]; ok {
			return v
		}
		return def
	}
	d := sigData{
		FirstName: pick("firstName", b.FirstName), LastName: pick("lastName", b.LastName), Position: in["position"],
		Phone: pick("phone", b.Phone), Fax: in["fax"], Mobile: pick("mobile", b.Mobile), Email: pick("email", b.Email), Room: pick("room", b.Room),
	}
	unit := func(k, def string, lv int) int64 {
		id, _ := strconv.ParseInt(pick(k, def), 10, 64)
		if u, ok := o.units[id]; ok && lv > 0 && o.depth[u.ID] == lv {
			return id
		}
		return 0
	}
	d.AmtID = unit("amt", b.Amt, c.AmtLevel)
	d.SGID = unit("sg", b.SG, c.SGLevel)
	if d.SGID != 0 && (d.AmtID == 0 || !o.within(d.SGID, d.AmtID)) {
		d.SGID = 0 // Sachgebiet gehört nicht zum gewählten Amt
	}
	d.Amt, d.SG = o.units[d.AmtID].Name, o.units[d.SGID].Name
	if d.AmtID == 0 {
		d.Amt = ""
	}
	if d.SGID == 0 {
		d.SG = ""
	}
	if d.Email == "" {
		d.Email = c.mailFromName(d.FirstName, d.LastName)
	}
	site := pick("site", b.Site)
	if site == sigSiteFree {
		d.Site = SigSite{ID: sigSiteFree, Extra: in["freeExtra"], Street: in["freeStreet"], Zip: in["freeZip"], City: in["freeCity"]}
	} else if s, ok := c.site(site); ok {
		d.Site = s
	} else if s, ok := c.site(c.DefaultSite); ok {
		d.Site = s
	}
	return d
}

var sigUmlaut = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
var reSigTitle = regexp.MustCompile(`\b(dr|prof)\.\s*`)

// mailFromName: vorname.nachname@domain (wie im bisherigen Generator)
func (c SigConfig) mailFromName(first, last string) string {
	if c.MailDomain == "" {
		return ""
	}
	part := func(s string) string {
		s = sigUmlaut.Replace(reSigTitle.ReplaceAllString(strings.ToLower(s), ""))
		var b strings.Builder
		dash := false
		for _, r := range strings.TrimSpace(foldAccents(s)) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
				b.WriteRune(r)
				dash = false
			case unicode.IsSpace(r):
				if !dash {
					b.WriteRune('-')
					dash = true
				}
			}
		}
		return strings.Trim(b.String(), "-.")
	}
	f, l := part(first), part(last)
	if f == "" || l == "" {
		return ""
	}
	return f + "." + l + "@" + c.MailDomain
}

var sigAccents = strings.NewReplacer("á", "a", "à", "a", "â", "a", "å", "a", "ã", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "õ", "o", "ø", "o", "ú", "u", "ù", "u", "û", "u",
	"ç", "c", "ñ", "n", "ý", "y", "ÿ", "y", "š", "s", "ž", "z", "č", "c", "ć", "c", "ł", "l", "ę", "e", "ą", "a", "ś", "s", "ź", "z", "ż", "z", "ğ", "g", "ı", "i", "ş", "s")

func foldAccents(s string) string { return sigAccents.Replace(s) }

// ---------- Schreibweise nach Styleguide ----------

func sigDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || r == '+' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case strings.HasPrefix(d, "+49"):
		d = "0" + d[3:]
	case strings.HasPrefix(d, "0049"):
		d = "0" + d[4:]
	}
	return strings.ReplaceAll(d, "+", "")
}

// landline: „01234 56-1104“ – Ortsvorwahl, Leerzeichen, Anlage, Bindestrich, Durchwahl.
// Andere Vorwahlen bleiben, wie eingegeben.
func (c SigConfig) landline(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	d := sigDigits(raw)
	if c.AreaCode != "" && strings.HasPrefix(d, c.AreaCode) {
		d = d[len(c.AreaCode):]
	} else if strings.HasPrefix(d, "0") || c.AreaCode == "" {
		return raw
	}
	if d == "" {
		return ""
	}
	if c.Exchange != "" && strings.HasPrefix(d, c.Exchange) && len(d) > len(c.Exchange) {
		d = c.Exchange + "-" + d[len(c.Exchange):]
	}
	return c.AreaCode + " " + d
}

var reSigMobile = regexp.MustCompile(`^01[5-7]\d{7,9}$`)

// mobile: „0172 232 08 63“ – die letzten vier Ziffern in Zweiergruppen
func sigMobile(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	d := sigDigits(raw)
	if !strings.HasPrefix(d, "0") {
		d = "0" + d
	}
	if !reSigMobile.MatchString(d) {
		return raw
	}
	rest := d[4:]
	var pairs []string
	for len(rest) > 3 && len(pairs) < 2 {
		pairs = append([]string{rest[len(rest)-2:]}, pairs...)
		rest = rest[:len(rest)-2]
	}
	return strings.Join(append([]string{d[:4], rest}, pairs...), " ")
}

func sigRoom(z string) string {
	if z != "" && z[0] >= '0' && z[0] <= '9' {
		return "Zimmer " + z
	}
	return z
}

// ---------- HTML für Outlook und Klartext ----------

type sigOut struct {
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
	Missing []string `json:"missing"`
}

func (c SigConfig) render(d sigData, logo []byte, preview bool) sigOut {
	esc := html.EscapeString
	font := fmt.Sprintf("font-family:%s;font-size:%s;line-height:%s;mso-line-height-rule:exactly;color:%s;", c.Font, c.FontSize, c.LineHeight, c.Color)
	table := `<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="border-collapse:collapse;mso-table-lspace:0pt;mso-table-rspace:0pt;">`
	row := func(inner string) string { return `<tr><td style="` + font + `">` + inner + `</td></tr>` }
	gap := func(px int) string {
		return fmt.Sprintf(`<tr><td style="height:%dpx;font-size:1px;line-height:1px;mso-line-height-rule:exactly;">&nbsp;</td></tr>`, px)
	}
	link := func(href, text string) string {
		return `<a href="` + esc(href) + `" style="color:` + c.Color + `;text-decoration:none;">` + esc(text) + `</a>`
	}
	ph := func(text string) string {
		if preview {
			return `<span style="color:#A5A8A4;">` + esc(text) + `</span>`
		}
		return ""
	}
	nonEmpty := func(xs ...string) []string {
		var out []string
		for _, x := range xs {
			if x != "" {
				out = append(out, x)
			}
		}
		return out
	}

	name := strings.TrimSpace(d.FirstName + " " + d.LastName)
	tel, fax, mob := c.landline(d.Phone), c.landline(d.Fax), sigMobile(d.Mobile)
	room := sigRoom(d.Room)
	mail := strings.ReplaceAll(d.Email, " ", "")

	var missing []string
	if d.FirstName == "" {
		missing = append(missing, "Vorname")
	}
	if d.LastName == "" {
		missing = append(missing, "Nachname")
	}
	if mail == "" {
		missing = append(missing, "E-Mail-Adresse")
	}
	if d.Site.ID == sigSiteFree && (d.Site.Street == "" || d.Site.Zip == "" || d.Site.City == "") {
		missing = append(missing, "Adresse des Dienstgebäudes")
	}

	nameHTML := ph("Vorname Nachname")
	if name != "" {
		nameHTML = esc(name)
	}
	person := strings.Join(nonEmpty("<b>"+nameHTML+"</b>", esc(d.Amt), esc(d.SG), esc(d.Position)), "<br>")

	type num struct{ label, n string }
	var nums []num
	for _, x := range []num{{"Tel.", tel}, {"Fax", fax}, {"Mobil", mob}} {
		if x.n != "" {
			nums = append(nums, x)
		}
	}
	// gemeinsame Fluchtlinie: „Tel.“/„Fax“ ≈ 24 px, „Mobil“ ≈ 35 px in Arial 11 pt, plus Wortabstand
	col := 29
	if mob != "" {
		col = 40
	}
	numsHTML := ""
	if len(nums) > 0 {
		var b strings.Builder
		b.WriteString(`<tr><td style="padding:0;">` + table)
		for _, x := range nums {
			fmt.Fprintf(&b, `<tr><td width="%d" style="%swidth:%dpx;padding:0;white-space:nowrap;">%s</td><td style="%swhite-space:nowrap;">%s</td></tr>`, col, font, col, x.label, font, esc(x.n))
		}
		b.WriteString(`</table></td></tr>`)
		numsHTML = b.String()
	}
	mailHTML := ph("vorname.nachname@" + c.MailDomain)
	if mail != "" {
		mailHTML = link("mailto:"+mail, mail)
	}

	s := d.Site
	street := ph("Straße und Hausnummer")
	if s.Street != "" {
		street = esc(s.Street)
	}
	city := ph("PLZ und Ort")
	if zc := strings.TrimSpace(s.Zip + " " + s.City); zc != "" {
		city = esc(zc)
	}
	extra := esc(strings.Join(nonEmpty(s.Extra, room), ", "))
	addr := strings.Join(nonEmpty("<b>"+esc(c.Org)+"</b>", extra, street, city, esc(c.PostAddress)), "<br>")

	var links []string
	for _, l := range c.Links {
		links = append(links, link(l.URL, l.Text))
	}

	var b strings.Builder
	b.WriteString(table)
	if len(logo) > 0 {
		fmt.Fprintf(&b, `<tr><td style="padding:0;"><img src="data:image/png;base64,%s" width="%d" height="%d" alt="%s" style="display:block;width:%dpx;height:%dpx;border:0;outline:none;text-decoration:none;"></td></tr>`,
			base64.StdEncoding.EncodeToString(logo), c.LogoWidth, c.LogoHeight, esc(c.LogoAlt), c.LogoWidth, c.LogoHeight)
		b.WriteString(gap(16))
	}
	b.WriteString(row(person))
	b.WriteString(gap(14))
	b.WriteString(numsHTML)
	if mailHTML != "" {
		b.WriteString(row(mailHTML))
	}
	b.WriteString(gap(14))
	b.WriteString(row(addr))
	if len(links) > 0 {
		b.WriteString(gap(14))
		b.WriteString(row(strings.Join(links, "<br>")))
	}
	b.WriteString(`</table>`)

	// Klartext (für Programme ohne HTML)
	t := nonEmpty(name, d.Amt, d.SG, d.Position)
	t = append(t, "")
	for _, x := range nums {
		t = append(t, x.label+" "+x.n)
	}
	if mail != "" {
		t = append(t, mail)
	}
	t = append(t, "", c.Org)
	if x := strings.Join(nonEmpty(s.Extra, room), ", "); x != "" {
		t = append(t, x)
	}
	t = append(t, nonEmpty(s.Street, strings.TrimSpace(s.Zip+" "+s.City), c.PostAddress)...)
	if len(c.Links) > 0 {
		t = append(t, "")
		for _, l := range c.Links {
			t = append(t, l.Text)
		}
	}
	if missing == nil {
		missing = []string{}
	}
	return sigOut{HTML: b.String(), Text: strings.Join(t, "\r\n"), Missing: missing}
}

// ---------- Auswahllisten ----------

type sigUnitOpt struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short,omitempty"`
	Group string `json:"group,omitempty"` // Einheit darüber (z. B. Dezernat)
	Amt   string `json:"amt,omitempty"`   // bei Sachgebieten: zugehöriges Amt
}

func (c SigConfig) options(o sigOrg, units []OrgUnit) (amts, sgs []sigUnitOpt) {
	sorted := append([]OrgUnit(nil), units...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Sort != sorted[j].Sort {
			return sorted[i].Sort < sorted[j].Sort
		}
		return collatorLess(sorted[i].Name, sorted[j].Name)
	})
	for _, u := range sorted {
		d := o.depth[u.ID]
		switch {
		case c.AmtLevel > 0 && d == c.AmtLevel:
			amts = append(amts, sigUnitOpt{ID: strconv.FormatInt(u.ID, 10), Name: u.Name, Short: u.Short, Group: o.units[u.Parent].Name})
		case c.SGLevel > 0 && d == c.SGLevel:
			if amt := o.ancestorAt(u.ID, c.AmtLevel); amt != 0 || c.AmtLevel == 0 {
				sgs = append(sgs, sigUnitOpt{ID: strconv.FormatInt(u.ID, 10), Name: u.Name, Short: u.Short, Amt: strconv.FormatInt(amt, 10)})
			}
		}
	}
	if amts == nil {
		amts = []sigUnitOpt{}
	}
	if sgs == nil {
		sgs = []sigUnitOpt{}
	}
	return
}

func collatorLess(a, b string) bool {
	return strings.ToLower(foldAccents(sigUmlaut.Replace(strings.ToLower(a)))) < strings.ToLower(foldAccents(sigUmlaut.Replace(strings.ToLower(b))))
}

// ---------- Endpunkte ----------

func (a *App) sigContext(ctx context.Context) (SigConfig, sigOrg, []OrgUnit, error) {
	c := a.sigConfig(ctx)
	units, err := a.st().ListOrgUnits(ctx)
	if err != nil {
		return c, sigOrg{}, nil, err
	}
	c.UnitSites = resolveUnitSites(c.UnitSites, units)
	return c, newSigOrg(units, len(a.orgLevels(ctx))), units, nil
}

// resolveUnitSites: Schlüssel als Kürzel auf die IDs der Einheiten abbilden (eine ID hat Vorrang)
func resolveUnitSites(m map[string]string, units []OrgUnit) map[string]string {
	out := map[string]string{}
	for _, u := range units {
		id := strconv.FormatInt(u.ID, 10)
		if v, ok := m[id]; ok {
			out[id] = v
		} else if v, ok := m[u.Short]; ok && u.Short != "" {
			out[id] = v
		}
	}
	return out
}

// publicSig: Grunddaten ohne interne Zuordnungen
func (c SigConfig) public() map[string]any {
	return map[string]any{
		"org": c.Org, "areaCode": c.AreaCode, "exchange": c.Exchange, "mailDomain": c.MailDomain, "sites": c.Sites,
		"amtLevel": c.AmtLevel, "sgLevel": c.SGLevel, "stand": c.Stand, "note": c.Note, "logoWidth": c.LogoWidth, "logoHeight": c.LogoHeight,
	}
}

// GET /api/signature – Grunddaten, Auswahllisten, Werte aus dem Eintrag und eigene Angaben
func (a *App) handleGetSignature(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	ctx := r.Context()
	c, o, units, err := a.sigContext(ctx)
	if err != nil {
		dbFail(w, err)
		return
	}
	if !c.Enabled {
		jsonError(w, http.StatusNotFound, "Die Signatur ist abgeschaltet")
		return
	}
	base, has := a.sigBaseFor(ctx, s, c, o, units)
	amts, sgs := c.options(o, units)
	in := a.loadPrefs(ctx, s.Username).Signature
	if in == nil {
		in = map[string]string{}
	}
	lv := a.orgLevels(ctx)
	name := func(i int) string {
		if i > 0 && i <= len(lv) {
			return lv[i-1].Name
		}
		return ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config": c.public(), "amtLabel": name(c.AmtLevel), "sgLabel": name(c.SGLevel),
		"amts": amts, "sgs": sgs, "base": base, "hasEntry": has, "input": in,
	})
}

// PUT /api/signature – eigene Angaben speichern
func (a *App) handleSetSignature(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	var body struct {
		Input map[string]string `json:"input"`
	}
	if !decodeJSON(w, r, &body, 16<<10) {
		return
	}
	in, err := cleanSigInput(body.Input)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	if !a.sigConfig(ctx).Enabled {
		jsonError(w, http.StatusNotFound, "Die Signatur ist abgeschaltet")
		return
	}
	p := a.loadPrefs(ctx, s.Username)
	p.Signature = in
	if len(in) == 0 {
		p.Signature = nil
	}
	if err := a.savePrefs(ctx, s.Username, p); err != nil {
		dbFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/signature/render – Signatur zu den (noch nicht gespeicherten) Angaben; ändert nichts.
// html/text = genau das, was kopiert wird; previewHtml (mit preview) zeigt leere Felder als Platzhalter.
func (a *App) handleRenderSignature(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	var body struct {
		Input   map[string]string `json:"input"`
		Preview bool              `json:"preview"`
	}
	if !decodeJSON(w, r, &body, 16<<10) {
		return
	}
	in, err := cleanSigInput(body.Input)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	c, o, units, err := a.sigContext(ctx)
	if err != nil {
		dbFail(w, err)
		return
	}
	if !c.Enabled {
		jsonError(w, http.StatusNotFound, "Die Signatur ist abgeschaltet")
		return
	}
	base, _ := a.sigBaseFor(ctx, s, c, o, units)
	logo, _ := a.sigLogo(ctx)
	d := c.merge(o, base, in)
	out := c.render(d, logo, false)
	res := map[string]any{"html": out.HTML, "text": out.Text, "missing": out.Missing}
	if body.Preview { // Vorschau mit Platzhaltern für leere Felder
		res["previewHtml"] = c.render(d, logo, true).HTML
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

// GET /api/signature/logo.png – Logo als Datei (zum Einfügen in ältere Outlook-Versionen)
func (a *App) handleSignatureLogo(w http.ResponseWriter, r *http.Request) {
	b, _ := a.sigLogo(r.Context())
	if len(b) == 0 {
		jsonError(w, http.StatusNotFound, "Kein Logo hinterlegt")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="Signatur-Logo.png"`)
	}
	w.Write(b)
}

// GET /signature-frame.html – Vorschau in einem eigenen Dokument: Die Signatur braucht
// style-Attribute (Outlook), die die Hauptseite per CSP verbietet. Der Rahmen darf sie, aber
// nichts nachladen und kein fremdes Skript ausführen.
func (a *App) handleSignatureFrame(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/signature-frame.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.Write(b)
}

// ---------- Verwaltung ----------

func (a *App) handleGetSignatureAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, o, units, err := a.sigContext(ctx)
	if err != nil {
		dbFail(w, err)
		return
	}
	logo, custom := a.sigLogo(ctx)
	info := map[string]any{"custom": custom, "bytes": len(logo), "fromFile": !custom && len(logo) > 0}
	if cfg, err := png.DecodeConfig(bytes.NewReader(logo)); err == nil {
		info["width"], info["height"] = cfg.Width, cfg.Height
	}
	// Einheiten zur Zuordnung der Dienstgebäude: bis einschließlich Sachgebiets-Ebene
	type unitRow struct {
		ID     string `json:"id"`
		Parent string `json:"parent,omitempty"`
		Name   string `json:"name"`
		Short  string `json:"short,omitempty"`
		Depth  int    `json:"depth"`
		Path   string `json:"path"`
	}
	// Baumreihenfolge: Einheit, dann ihre Untereinheiten (nach Sortierung und Name)
	kids := map[int64][]OrgUnit{}
	for _, u := range units {
		p := u.Parent
		if _, ok := o.units[p]; !ok {
			p = 0
		}
		kids[p] = append(kids[p], u)
	}
	for k := range kids {
		l := kids[k]
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].Sort != l[j].Sort {
				return l[i].Sort < l[j].Sort
			}
			return collatorLess(l[i].Name, l[j].Name)
		})
	}
	var rows []unitRow
	seen := map[int64]bool{}
	var walk func(p int64, path []string)
	walk = func(p int64, path []string) {
		for _, u := range kids[p] {
			if seen[u.ID] {
				continue
			}
			seen[u.ID] = true
			par := ""
			if u.Parent != 0 {
				par = strconv.FormatInt(u.Parent, 10)
			}
			rows = append(rows, unitRow{ID: strconv.FormatInt(u.ID, 10), Parent: par, Name: u.Name, Short: u.Short, Depth: o.depth[u.ID], Path: strings.Join(path, " › ")})
			walk(u.ID, append(append([]string(nil), path...), u.Name))
		}
	}
	walk(0, nil)
	if rows == nil {
		rows = []unitRow{}
	}
	file := ""
	if a.sigFile != nil {
		file = a.sigFile.path
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": c, "logo": info, "levels": a.orgLevels(ctx), "units": rows, "source": a.sigSource(ctx), "file": file})
}

func (a *App) handleSetSignatureAdmin(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	var in SigConfig
	if !decodeJSON(w, r, &in, 128<<10) {
		return
	}
	ctx := r.Context()
	c, err := a.cleanSigConfig(ctx, in)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := json.Marshal(c)
	if len(b) > 60<<10 {
		jsonError(w, http.StatusBadRequest, "Zu viele Angaben (Dienstgebäude/Zuordnungen kürzen)")
		return
	}
	if err := a.st().SetSetting(ctx, settingSignature, string(b)); err != nil {
		dbFail(w, err)
		return
	}
	a.audit(r, s.Username, "signature_settings", "", fmt.Sprintf("an=%v, %d Dienstgebäude, %d Zuordnungen", c.Enabled, len(c.Sites), len(c.UnitSites)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": c})
}

// PUT /api/admin/signature/logo {png: Base64} – nur PNG, wird neu kodiert (keine Metadaten)
func (a *App) handleSetSignatureLogo(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	var in struct {
		PNG string `json:"png"`
	}
	if !decodeJSON(w, r, &in, 128<<10) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.PNG)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Kein gültiges Bild")
		return
	}
	clean, wd, ht, err := cleanSigLogo(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	if err := a.st().SetSetting(ctx, settingSignatureLogo, base64.StdEncoding.EncodeToString(clean)); err != nil {
		dbFail(w, err)
		return
	}
	a.audit(r, s.Username, "signature_logo", "", fmt.Sprintf("%d × %d, %d Bytes", wd, ht, len(clean)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "width": wd, "height": ht, "bytes": len(clean)})
}

// cleanSigLogo: nur PNG, neu kodiert (ohne Metadaten), höchstens sigLogoMaxBytes
func cleanSigLogo(raw []byte) ([]byte, int, int, error) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, errors.New("Bitte eine PNG-Datei verwenden")
	}
	bd := img.Bounds()
	if bd.Dx() > 1200 || bd.Dy() > 600 {
		return nil, 0, 0, errors.New("Logo zu groß: höchstens 1200 × 600 Pixel (empfohlen: doppelte Anzeigegröße, z. B. 310 × 90)")
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, img); err != nil {
		return nil, 0, 0, errors.New("Bild lässt sich nicht verarbeiten")
	}
	if out.Len() > sigLogoMaxBytes {
		return nil, 0, 0, fmt.Errorf("Logo zu groß (%d KB, höchstens %d KB) – es geht mit jeder Mail mit", out.Len()>>10, sigLogoMaxBytes>>10)
	}
	return out.Bytes(), bd.Dx(), bd.Dy(), nil
}

// DELETE /api/admin/signature – Einstellungen der Verwaltung verwerfen (dann gilt die Datei)
func (a *App) handleResetSignatureAdmin(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	if err := a.st().SetSetting(r.Context(), settingSignature, ""); err != nil {
		dbFail(w, err)
		return
	}
	a.audit(r, s.Username, "signature_settings", "", "zurückgesetzt")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "source": a.sigSource(r.Context())})
}

func (a *App) handleDeleteSignatureLogo(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	if err := a.st().SetSetting(r.Context(), settingSignatureLogo, ""); err != nil {
		dbFail(w, err)
		return
	}
	a.audit(r, s.Username, "signature_logo", "", "Standard")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
