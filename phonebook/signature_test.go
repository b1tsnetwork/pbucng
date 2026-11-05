package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignatureFormatting(t *testing.T) {
	c := exampleSig(t).cfg
	for in, want := range map[string]string{
		"56-1104": "01234 56-1104", "561104": "01234 56-1104", "01234 56 1104": "01234 56-1104",
		"+49 1234 56-1104": "01234 56-1104", "0049 1234 561104": "01234 56-1104",
		"0211 123456": "0211 123456", "  ": "", "4711": "01234 4711",
	} {
		if got := c.landline(in); got != want {
			t.Errorf("Festnetz %q: %q, erwartet %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"01722320863": "0172 232 08 63", "+49 172 2320863": "0172 232 08 63", "0151 12345678": "0151 1234 56 78",
		"12345": "12345", "": "",
	} {
		if got := sigMobile(in); got != want {
			t.Errorf("Mobil %q: %q, erwartet %q", in, got, want)
		}
	}
	for _, x := range [][3]string{
		{"Jörg", "Müller-Lüdenscheidt", "joerg.mueller-luedenscheidt@musterstadt.de"},
		{"Dr. Anna Maria", "Schmitz", "anna-maria.schmitz@musterstadt.de"},
		{"Zoë", "Çelik", "zoe.celik@musterstadt.de"},
		{"", "Schmitz", ""},
	} {
		if got := c.mailFromName(x[0], x[1]); got != x[2] {
			t.Errorf("Adresse aus %q %q: %q, erwartet %q", x[0], x[1], got, x[2])
		}
	}
	if sigRoom("414") != "Zimmer 414" || sigRoom("Foyer") != "Foyer" {
		t.Error("Zimmer")
	}
}

type sigEnv struct {
	a                  *App
	h                  http.Handler
	dez, amt, sg, team int64
	amt2, sg2          int64
	anna               int64
}

func newSigEnv(t *testing.T) *sigEnv {
	t.Helper()
	a := testApp(t)
	ctx := context.Background()
	st := a.st()
	e := &sigEnv{a: a, h: a.routes(false)}
	unit := func(name, short string, parent int64) int64 {
		id, err := st.CreateOrgUnit(ctx, OrgUnit{Name: name, Short: short, Parent: parent})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.dez = unit("Dezernat II", "II", 0)
	e.amt = unit("Ordnungsamt", "32", e.dez)
	e.sg = unit("Allgemeine Ordnungsangelegenheiten", "32.1", e.amt)
	e.team = unit("Team Gewerbe", "32.1.1", e.sg)
	e.amt2 = unit("Feuerwehr", "37", e.dez)
	e.sg2 = unit("Rettungsdienst", "37.3", e.amt2)
	now := time.Now().UTC()
	id, err := st.CreateEmployee(ctx, &EmployeeRec{Username: "anna", LastName: "Albers", FirstName: "Anna", Phone: "56-1104", Mobile: "01722320863",
		Room: "414", Email: "anna.albers@musterstadt.de", Source: "manual", Active: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	e.anna = id
	st.SetOrgUnit(ctx, []int64{id}, e.team)
	e.a.sigFile = exampleSig(t)
	e.a.sigFile.logo = pngOf(t, 310, 90, false)
	return e
}

// exampleSig: Grunddaten aus docs/signature.example.json (prüft zugleich das Beispiel)
func exampleSig(t *testing.T) *sigFile {
	t.Helper()
	f, err := loadSigFile("docs/signature.example.json")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (e *sigEnv) call(t *testing.T, method, path string, c *http.Cookie, body any) (int, map[string]any) {
	t.Helper()
	hdr := map[string]string{}
	if c != nil {
		hdr["Cookie"] = c.String()
		var me map[string]any
		json.Unmarshal(getWith(e.h, "/api/me", c, "10.0.0.5").Body.Bytes(), &me)
		if s, ok := me["csrf"].(string); ok {
			hdr["X-CSRF-Token"] = s
		}
	}
	b := ""
	if body != nil {
		x, _ := json.Marshal(body)
		b = string(x)
	}
	rec := doReq(e.h, method, path, b, "10.0.0.5", hdr)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSignatureFromEntryAndOrg(t *testing.T) {
	e := newSigEnv(t)
	ctx := context.Background()
	// Dienstgebäude je Einheit aus der Datei: Kürzel „32“ (Ordnungsamt) → Markt 5, gilt auch darunter
	c := e.a.sigFile.cfg

	anna := sessionCookie(t, e.a, "anna", false)
	if code, _ := e.call(t, "GET", "/api/signature", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("ohne Anmeldung: %d", code)
	}
	code, out := e.call(t, "GET", "/api/signature", anna, nil)
	if code != 200 || out["hasEntry"] != true || out["amtLabel"] != "Amt" || out["sgLabel"] != "Sachgebiet" {
		t.Fatalf("GET: %d %v", code, out)
	}
	base := out["base"].(map[string]any)
	if base["firstName"] != "Anna" || base["amt"] != strconv.FormatInt(e.amt, 10) || base["sg"] != strconv.FormatInt(e.sg, 10) || base["site"] != "markt5" {
		t.Fatalf("Werte aus dem Eintrag: %v", base)
	}
	if n := len(out["amts"].([]any)); n != 2 {
		t.Fatalf("Ämter: %d", n)
	}
	if n := len(out["sgs"].([]any)); n != 2 {
		t.Fatalf("Sachgebiete: %d", n)
	}

	render := func(in map[string]string) sigOut {
		t.Helper()
		code, out := e.call(t, "POST", "/api/signature/render", anna, map[string]any{"input": in})
		if code != 200 {
			t.Fatalf("render: %d %v", code, out)
		}
		var s sigOut
		x, _ := json.Marshal(out)
		json.Unmarshal(x, &s)
		return s
	}
	s := render(nil)
	for _, want := range []string{"<b>Anna Albers</b><br>Ordnungsamt<br>Allgemeine Ordnungsangelegenheiten", "01234 56-1104", "0172 232 08 63",
		"mailto:anna.albers@musterstadt.de", "Zimmer 414", "Markt 5", "12345 Musterstadt", "Postfach 10 20 30", "data:image/png;base64,", "www.musterstadt.de"} {
		if !strings.Contains(s.HTML, want) {
			t.Fatalf("HTML ohne %q:\n%s", want, s.HTML)
		}
	}
	if len(s.Missing) != 0 || !strings.Contains(s.Text, "Tel. 01234 56-1104\r\n") || strings.Contains(s.Text, "<") {
		t.Fatalf("Klartext/Pflicht: %v\n%s", s.Missing, s.Text)
	}

	// ergänzen und abweichend setzen; HTML wird maskiert
	s = render(map[string]string{"position": `Sachbearbeitung <script>alert(1)</script>`, "fax": "56-601", "room": "", "site": "frei",
		"freeExtra": "Stadtarchiv", "freeStreet": "Am Rathaus 1", "freeZip": "12345", "freeCity": "Musterstadt"})
	if strings.Contains(s.HTML, "<script>") || !strings.Contains(s.HTML, "&lt;script&gt;") || !strings.Contains(s.HTML, ">Fax<") ||
		strings.Contains(s.HTML, "Zimmer 414") || !strings.Contains(s.HTML, "Stadtarchiv<br>Am Rathaus 1") {
		t.Fatalf("Ergänzungen: %s", s.HTML)
	}
	// Sachgebiet aus einem anderen Amt passt nicht → entfällt; Amt ohne Eintrag → Platzhalter nur in der Vorschau
	s = render(map[string]string{"sg": strconv.FormatInt(e.sg2, 10)})
	if strings.Contains(s.HTML, "Rettungsdienst") || !strings.Contains(s.HTML, "Ordnungsamt") {
		t.Fatalf("fremdes Sachgebiet: %s", s.HTML)
	}
	s = render(map[string]string{"amt": strconv.FormatInt(e.amt2, 10), "sg": strconv.FormatInt(e.sg2, 10)})
	if !strings.Contains(s.HTML, "Feuerwehr<br>Rettungsdienst") {
		t.Fatalf("anderes Amt: %s", s.HTML)
	}
	s = render(map[string]string{"email": "", "firstName": "", "site": "frei"})
	if strings.Join(s.Missing, ",") != "Vorname,E-Mail-Adresse,Adresse des Dienstgebäudes" {
		t.Fatalf("fehlt: %v", s.Missing)
	}
	// E-Mail leer → aus dem Namen gebildet
	s = render(map[string]string{"email": "", "firstName": "Jörg"})
	if !strings.Contains(s.HTML, "joerg.albers@musterstadt.de") {
		t.Fatal("Adresse aus dem Namen")
	}

	// speichern und wieder laden; unbekannte Schlüssel fallen weg
	if code, out := e.call(t, "PUT", "/api/signature", anna, map[string]any{"input": map[string]string{"position": "Leitung", "hack": "x"}}); code != 200 {
		t.Fatalf("PUT: %d %v", code, out)
	}
	_, out = e.call(t, "GET", "/api/signature", anna, nil)
	in := out["input"].(map[string]any)
	if in["position"] != "Leitung" || in["hack"] != nil || len(in) != 1 {
		t.Fatalf("gespeichert: %v", in)
	}
	if code, _ := e.call(t, "PUT", "/api/signature", anna, map[string]any{"input": map[string]string{"amt": "abc"}}); code != 400 {
		t.Fatalf("ungültige Einheit: %d", code)
	}

	// ohne Telefonbucheintrag: leeres Formular, Vorschau mit Platzhaltern
	bert := sessionCookie(t, e.a, "bert", false)
	_, out = e.call(t, "GET", "/api/signature", bert, nil)
	if out["hasEntry"] != false {
		t.Fatalf("ohne Eintrag: %v", out)
	}
	code, out = e.call(t, "POST", "/api/signature/render", bert, map[string]any{"preview": true})
	if code != 200 || !strings.Contains(out["previewHtml"].(string), "Vorname Nachname") || strings.Contains(out["html"].(string), "Vorname Nachname") {
		t.Fatalf("Platzhalter: %d %v", code, out)
	}

	// Rückfall-Betrieb (nur lesen): Vorschau geht weiter, Speichern nicht
	e.a.offline.Store(true)
	if code, _ := e.call(t, "POST", "/api/signature/render", anna, map[string]any{}); code != 200 {
		t.Fatalf("Vorschau im Rückfall-Betrieb: %d", code)
	}
	if code, _ := e.call(t, "PUT", "/api/signature", anna, map[string]any{"input": map[string]string{}}); code != http.StatusServiceUnavailable {
		t.Fatalf("Speichern im Rückfall-Betrieb: %d", code)
	}
	e.a.offline.Store(false)

	// abgeschaltet
	c.Enabled = false
	b, _ := json.Marshal(c)
	e.a.st().SetSetting(ctx, settingSignature, string(b))
	if code, _ := e.call(t, "GET", "/api/signature", anna, nil); code != 404 {
		t.Fatalf("abgeschaltet: %d", code)
	}
}

func TestSignatureAdmin(t *testing.T) {
	e := newSigEnv(t)
	admin := sessionCookie(t, e.a, "chef", true)
	anna := sessionCookie(t, e.a, "anna", false)
	if code, _ := e.call(t, "GET", "/api/admin/signature", anna, nil); code != 403 {
		t.Fatalf("ohne Admin: %d", code)
	}
	code, out := e.call(t, "GET", "/api/admin/signature", admin, nil)
	if code != 200 || out["logo"].(map[string]any)["custom"] != false || len(out["units"].([]any)) != 6 {
		t.Fatalf("GET: %d %v", code, out)
	}
	var order []string
	for _, u := range out["units"].([]any) {
		order = append(order, u.(map[string]any)["name"].(string))
	}
	if strings.Join(order, "|") != "Dezernat II|Feuerwehr|Rettungsdienst|Ordnungsamt|Allgemeine Ordnungsangelegenheiten|Team Gewerbe" {
		t.Fatalf("Reihenfolge: %v", order)
	}
	cfgOK := func() SigConfig {
		c := exampleSig(t).cfg
		c.UnitSites = map[string]string{strconv.FormatInt(e.amt, 10): "feuerwache", "999": "rathaus", strconv.FormatInt(e.sg, 10): "gibtsnicht"}
		return c
	}
	for what, mod := range map[string]func(*SigConfig){
		"Farbe":         func(c *SigConfig) { c.Color = "red;background:url(x)" },
		"Schrift":       func(c *SigConfig) { c.Font = "Arial;}</style>" },
		"Link":          func(c *SigConfig) { c.Links = append(c.Links, SigLink{Text: "x", URL: "javascript:alert(1)"}) },
		"Vorwahl":       func(c *SigConfig) { c.AreaCode = "1234" },
		"Ebenen":        func(c *SigConfig) { c.AmtLevel, c.SGLevel = 3, 2 },
		"Doppelte ID":   func(c *SigConfig) { c.Sites = append(c.Sites, c.Sites[0]) },
		"Organisation":  func(c *SigConfig) { c.Org = "" },
		"Logo-Größe":    func(c *SigConfig) { c.LogoWidth = 5000 },
		"Kennung frei":  func(c *SigConfig) { c.Sites[0].ID = sigSiteFree },
		"Ebene zu tief": func(c *SigConfig) { c.SGLevel = 9 },
	} {
		c := cfgOK()
		mod(&c)
		if code, out := e.call(t, "PUT", "/api/admin/signature", admin, c); code != 400 {
			t.Errorf("%s: %d %v", what, code, out)
		}
	}
	if code, _ := e.call(t, "PUT", "/api/admin/signature", anna, cfgOK()); code != 403 {
		t.Fatalf("PUT ohne Admin: %d", code)
	}
	code, out = e.call(t, "PUT", "/api/admin/signature", admin, cfgOK())
	if code != 200 {
		t.Fatalf("PUT: %d %v", code, out)
	}
	us := out["config"].(map[string]any)["unitSites"].(map[string]any)
	if len(us) != 1 || us[strconv.FormatInt(e.amt, 10)] != "feuerwache" {
		t.Fatalf("Zuordnungen bereinigt: %v", us)
	}
	_, out = e.call(t, "GET", "/api/signature", anna, nil)
	if out["base"].(map[string]any)["site"] != "feuerwache" {
		t.Fatalf("Dienstgebäude geerbt: %v", out["base"])
	}

	// Verwaltung zurücksetzen: wieder die Datei
	if code, out := e.call(t, "DELETE", "/api/admin/signature", admin, nil); code != 200 || out["source"] != "file" {
		t.Fatalf("zurücksetzen: %d %v", code, out)
	}
	_, out = e.call(t, "GET", "/api/signature", anna, nil)
	if out["base"].(map[string]any)["site"] != "markt5" {
		t.Fatalf("nach dem Zurücksetzen: %v", out["base"])
	}

	// Logo: nur PNG, wird neu kodiert und in die Signatur übernommen
	img := image.NewRGBA(image.Rect(0, 0, 310, 90))
	for x := 0; x < 310; x++ {
		img.Set(x, 10, color.RGBA{200, 0, 0, 255})
	}
	var pb bytes.Buffer
	png.Encode(&pb, img)
	enc := base64.StdEncoding.EncodeToString
	if code, _ := e.call(t, "PUT", "/api/admin/signature/logo", admin, map[string]string{"png": enc([]byte("GIF89a"))}); code != 400 {
		t.Fatalf("kein PNG: %d", code)
	}
	if code, _ := e.call(t, "PUT", "/api/admin/signature/logo", anna, map[string]string{"png": enc(pb.Bytes())}); code != 403 {
		t.Fatalf("Logo ohne Admin: %d", code)
	}
	if code, out := e.call(t, "PUT", "/api/admin/signature/logo", admin, map[string]string{"png": enc(pngOf(t, 310, 90, true))}); code != 200 || out["width"] != float64(310) {
		t.Fatalf("Logo: %d %v", code, out)
	}
	logo, custom := e.a.sigLogo(context.Background())
	if !custom || bytes.Contains(logo, []byte("Geheim")) {
		t.Fatal("Logo nicht neu kodiert")
	}
	_, out = e.call(t, "POST", "/api/signature/render", anna, map[string]any{})
	if !strings.Contains(out["html"].(string), enc(logo)) {
		t.Fatal("neues Logo nicht in der Signatur")
	}
	if rec := getWith(e.h, "/api/signature/logo.png", anna, "10.0.0.5"); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), logo) || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("Logo als Datei: %d", rec.Code)
	}
	if code, _ := e.call(t, "DELETE", "/api/admin/signature/logo", admin, nil); code != 200 {
		t.Fatal("Logo zurücksetzen")
	}
	if logo, custom := e.a.sigLogo(context.Background()); custom || !bytes.Equal(logo, e.a.sigFile.logo) {
		t.Fatal("Standard-Logo")
	}
}

func TestSignatureFrameHeaders(t *testing.T) {
	a := testApp(t)
	h := a.routes(false)
	rec := getWith(h, "/signature-frame.html", nil, "10.0.0.5")
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != 200 || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(csp, "frame-ancestors 'self'") ||
		!strings.Contains(csp, "'unsafe-inline'") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "connect-src") {
		t.Fatalf("Rahmen: %d %q %q", rec.Code, rec.Header().Get("X-Frame-Options"), csp)
	}
	// die Hauptseite bleibt streng
	rec = getWith(h, "/", nil, "10.0.0.5")
	if rec.Header().Get("X-Frame-Options") != "DENY" || strings.Contains(rec.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("Hauptseite gelockert")
	}
}

func TestSignatureFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	os.WriteFile(filepath.Join(dir, "logo.png"), pngOf(t, 310, 90, true), 0o600)
	f, err := loadSigFile(write("ok.json", `{"enabled":true,"org":"Testamt","logoFile":"logo.png","sites":[{"id":"a","street":"Weg 1","zip":"1","city":"X"}]}`))
	if err != nil || len(f.logo) == 0 || bytes.Contains(f.logo, []byte("Geheim")) || f.cfg.Font == "" {
		t.Fatalf("Datei mit Logo: %v", err)
	}
	for name, body := range map[string]string{
		"tippfehler.json": `{"enabeld":true}`,
		"logo.json":       `{"logoFile":"fehlt.png"}`,
		"kaputt.json":     `{"org":`,
	} {
		if _, err := loadSigFile(write(name, body)); err == nil {
			t.Errorf("%s: kein Fehler", name)
		}
	}
	if _, err := loadSigFile(filepath.Join(dir, "gibtsnicht.json")); err == nil {
		t.Error("fehlende Datei")
	}
	// Inhalt wird wie in der Verwaltung geprüft
	a := testApp(t)
	a.sigFile, _ = loadSigFile(write("farbe.json", `{"enabled":true,"org":"X","color":"red"}`))
	if err := a.checkSigFile(context.Background()); err == nil {
		t.Error("ungültige Farbe in der Datei")
	}
	// ohne Datei: neutrale Vorgaben, Generator aus, kein Logo
	b := testApp(t)
	if b.sigSource(context.Background()) != "default" || b.sigConfig(context.Background()).Enabled || b.sigConfig(context.Background()).Org != "" {
		t.Fatal("neutrale Vorgaben")
	}
	if logo, _ := b.sigLogo(context.Background()); logo != nil {
		t.Fatal("Logo ohne Datei")
	}
	c := sessionCookie(t, b, "anna", false)
	if rec := getWith(b.routes(false), "/api/signature", c, "10.0.0.5"); rec.Code != 404 {
		t.Fatalf("ohne Grunddaten: %d", rec.Code)
	}
	if rec := getWith(b.routes(false), "/api/signature/logo.png", c, "10.0.0.5"); rec.Code != 404 {
		t.Fatalf("Logo-Datei ohne Logo: %d", rec.Code)
	}
}
