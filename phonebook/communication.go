package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Kommunikations-Add-on (UCNG), PH-01: Schalter, Prüfung der Konfiguration, Erreichbarkeit und
// Sicherheitsprofil. Ist das Add-on aus (Standard), registriert das Phonebook keine Add-on-Routen,
// startet keinen Hintergrund-Job und baut keine Verbindung zu UCNG auf (ADR-002).
// Betrieb auf eigener Subdomain (ADR-004); SSO folgt in PH-02.

type CommunicationFeatures struct {
	Chat           bool `json:"chat"`
	Media          bool `json:"media"`
	Voice          bool `json:"voice"`
	TemporaryRooms bool `json:"temporaryRooms"`
	Transcription  bool `json:"transcription"`
	Bots           bool `json:"bots"`
	Federation     bool `json:"federation"` // nur nach Sicherheitsfreigabe (ADR-009)
	Beacon         bool `json:"beacon"`     // nur nach Sicherheitsfreigabe (ADR-009)
}

type CommunicationConfig struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"` // "phonebook" (einziger Wert)
	// Adresse für den Browser – eigene Subdomain, z. B. https://kommunikation.firma.local
	PublicBaseURL string `json:"publicBaseURL"`
	// Adresse für Server-zu-Server-Aufrufe, z. B. http://ucng:8080 (internes Netz)
	InternalBaseURL string `json:"internalBaseURL"`
	// Gemeinsames Geheimnis für Backend-Aufrufe (ab PH-02), als Datei (Docker-Secret)
	ServiceCredentialFile string                `json:"serviceCredentialFile"`
	Features              CommunicationFeatures `json:"features"`
	// Aufbewahrung von Transkripten in Tagen; 0 (= unbegrenzt in UCNG) ist nicht erlaubt (ADR-011)
	DefaultTranscriptRetentionDays int      `json:"defaultTranscriptRetentionDays"`
	AutoCreateOrgRooms             bool     `json:"autoCreateOrgRooms"`
	OrgRoomLevels                  []int    `json:"orgRoomLevels"` // Ebenen mit Kanal (1 = oberste); leer = alle
	ProbeInterval                  Duration `json:"probeInterval"` // Prüfintervall, Standard 30s
	// PH-02: Nutzungsrecht, Rollen in UCNG, Nachprüfung des Verzeichnisstatus (commsso.go)
	Access    CommAccess `json:"access"`
	Roles     CommRoles  `json:"roles"`
	ADRecheck Duration   `json:"adRecheck"` // Standard 15m
	ADGrace   Duration   `json:"adGrace"`   // Standard 1h: so lange gilt der letzte Stand, wenn das AD nicht erreichbar ist

	secret string
	quiet  bool // keine Warnung beim Prüfen gespeicherter Einstellungen
}

// Standard: alles Übliche an, riskante Funktionen aus (Fahrplan §7)
var defaultCommunicationFeatures = CommunicationFeatures{Chat: true, Media: true, Voice: true, TemporaryRooms: true, Bots: true}

func (c *CommunicationConfig) validate(phonebookURL string, ldapOn bool) error {
	if !c.Enabled {
		return nil
	}
	if c.Mode == "" {
		c.Mode = "phonebook"
	}
	if c.Mode != "phonebook" {
		return errors.New(`communication.mode: nur "phonebook" wird unterstützt`)
	}
	pub, err := checkBaseURL(c.PublicBaseURL, "communication.publicBaseURL", true)
	if err != nil {
		return err
	}
	c.PublicBaseURL = pub.String()
	in, err := checkBaseURL(c.InternalBaseURL, "communication.internalBaseURL", false)
	if err != nil {
		return err
	}
	c.InternalBaseURL = in.String()
	if phonebookURL != "" {
		if po, ok := normOrigin(phonebookURL); ok && po == mustOrigin(pub) {
			return errors.New("communication.publicBaseURL: braucht eine eigene Subdomain (gleiche Herkunft wie publicURL – siehe ADR-004)")
		}
	}
	if c.ServiceCredentialFile == "" {
		return errors.New("communication.serviceCredentialFile fehlt (Datei mit dem gemeinsamen Geheimnis, z. B. /run/secrets/ucng_integration_secret)")
	}
	st, err := os.Stat(c.ServiceCredentialFile)
	if err != nil {
		return fmt.Errorf("communication.serviceCredentialFile: %w", err)
	}
	if !st.Mode().IsRegular() {
		return errors.New("communication.serviceCredentialFile: keine normale Datei")
	}
	if st.Mode().Perm()&0o004 != 0 {
		return errors.New("communication.serviceCredentialFile: für alle lesbar – bitte chmod 600 (bzw. 440 mit passender Gruppe)")
	}
	b, err := os.ReadFile(c.ServiceCredentialFile)
	if err != nil {
		return fmt.Errorf("communication.serviceCredentialFile: %w", err)
	}
	if c.secret = strings.TrimSpace(string(b)); len(c.secret) < 32 {
		return errors.New("communication.serviceCredentialFile: Geheimnis zu kurz (mindestens 32 Zeichen, z. B. openssl rand -base64 48)")
	}
	if c.DefaultTranscriptRetentionDays == 0 {
		c.DefaultTranscriptRetentionDays = 14
	}
	if c.DefaultTranscriptRetentionDays < 1 || c.DefaultTranscriptRetentionDays > 365 {
		return errors.New("communication.defaultTranscriptRetentionDays: 1–365 Tage (unbegrenzt ist nicht erlaubt, ADR-011)")
	}
	if c.ProbeInterval.Duration == 0 {
		c.ProbeInterval = Duration{30 * time.Second}
	}
	if c.ProbeInterval.Duration < 5*time.Second || c.ProbeInterval.Duration > time.Hour {
		return errors.New("communication.probeInterval: 5s bis 1h")
	}
	if c.Features.Federation || c.Features.Beacon {
		log.Printf("WARNUNG: communication.features.federation/beacon ist an – nur nach Sicherheitsfreigabe (ADR-009)")
	}
	return c.validateSSO(ldapOn)
}

// UnmarshalJSON setzt fehlende Funktionsschalter auf die Standardwerte.
func (f *CommunicationFeatures) UnmarshalJSON(b []byte) error {
	type plain CommunicationFeatures
	p := plain(defaultCommunicationFeatures)
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*f = CommunicationFeatures(p)
	return nil
}

func mustOrigin(u *url.URL) string {
	o, _ := normOrigin(u.String())
	return o
}

// checkBaseURL: absolute http(s)-Adresse ohne Zugangsdaten, Abfrage oder Fragment. Öffentlich:
// nur Wurzelpfad (Subdomain-Betrieb) und https – http nur für localhost (Entwicklung).
func checkBaseURL(raw, name string, public bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || raw == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%s: absolute Adresse mit http(s) erwartet", name)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("%s: ohne Zugangsdaten, Abfrage und Fragment angeben", name)
	}
	if public {
		if strings.Trim(u.Path, "/") != "" {
			return nil, fmt.Errorf("%s: nur die Wurzel einer eigenen Subdomain (ohne Pfad), siehe ADR-004", name)
		}
		if u.Scheme != "https" && !isLocalHost(u.Hostname()) {
			return nil, fmt.Errorf("%s: https erforderlich (Kamera/Mikrofon brauchen einen sicheren Kontext)", name)
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func isLocalHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---------- Prüfung von UCNG (Hintergrund) ----------

// commState: letztes Ergebnis der Prüfung
type commState struct {
	mu        sync.RWMutex
	checkedAt time.Time
	reachable bool
	secure    bool     // Sicherheitsprofil eingehalten
	problems  []string // Gründe, wenn nicht einsatzbereit
	latency   time.Duration
	version   string
	voice     bool
}

type commSnapshot struct {
	Status    string   `json:"status"` // ok, insecure, unreachable, pending
	Reachable bool     `json:"reachable"`
	Secure    bool     `json:"secure"`
	Problems  []string `json:"problems,omitempty"`
	CheckedAt string   `json:"checkedAt,omitempty"`
	LatencyMs float64  `json:"latencyMs,omitempty"`
	Version   string   `json:"version,omitempty"`
	Voice     bool     `json:"voice"`
}

func (c *commState) snapshot() commSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := commSnapshot{Reachable: c.reachable, Secure: c.secure, Problems: append([]string(nil), c.problems...),
		LatencyMs: float64(c.latency.Microseconds()) / 1000, Version: c.version, Voice: c.voice}
	switch {
	case c.checkedAt.IsZero():
		s.Status = "pending"
	case !c.reachable:
		s.Status = "unreachable"
	case !c.secure:
		s.Status = "insecure"
	default:
		s.Status = "ok"
	}
	if !c.checkedAt.IsZero() {
		s.CheckedAt = c.checkedAt.UTC().Format(time.RFC3339)
	}
	return s
}

// ucngSettings: die öffentlichen Felder von GET /api/settings, die das Sicherheitsprofil belegen
type ucngSettings struct {
	AppMode string `json:"appMode"`
	Hosted  *struct {
		Guests       string `json:"guests"`
		Registration bool   `json:"registration"`
		Federation   bool   `json:"federation"`
	} `json:"hosted"`
	ChanFed   *json.RawMessage `json:"chanFed"`
	Voice     bool             `json:"voice"`
	Phonebook *struct {
		Authorize string   `json:"authorize"`
		Locks     []string `json:"locks"` // aktive Betriebssperren (ADR-009/020)
		// ADR-011
		TranscriptKeepDays int  `json:"transcriptKeepDays"`
		STT                bool `json:"stt"`
	} `json:"phonebook"`
	About struct {
		Version string `json:"version"`
	} `json:"about"`
}

func commHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 4 * time.Second,
		// keine Weiterleitungen folgen: die interne Adresse ist fest konfiguriert
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (a *App) probeCommunication(ctx context.Context, client *http.Client) {
	cfg := a.cfg.Communication
	t0 := time.Now()
	var problems []string
	reachable := false
	var set ucngSettings
	get := func(path string, dst any) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.InternalBaseURL+path, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", "Telefonbuch/"+version+" (communication probe)")
		res, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
		if err != nil {
			return res.StatusCode, err
		}
		if dst != nil && res.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, dst); err != nil {
				return res.StatusCode, fmt.Errorf("Antwort ist kein JSON: %w", err)
			}
		}
		return res.StatusCode, nil
	}
	if code, err := get("/healthz", nil); err != nil || code != http.StatusOK {
		problems = append(problems, fmt.Sprintf("UCNG nicht erreichbar (%s/healthz: %s)", cfg.InternalBaseURL, errText(code, err)))
	} else if code, err := get("/api/settings", &set); err != nil || code != http.StatusOK {
		problems = append(problems, fmt.Sprintf("UCNG-Einstellungen nicht lesbar (%s)", errText(code, err)))
	} else {
		reachable = true
		problems = append(problems, commPolicyProblems(set, cfg.Features)...)
		problems = append(problems, commSSOProblems(set, a.cfg.PublicURL)...)
		problems = append(problems, commTranscriptProblems(set, &cfg)...)
	}
	lat := time.Since(t0)
	a.comm.mu.Lock()
	first := a.comm.checkedAt.IsZero()
	wasOK := a.comm.reachable && a.comm.secure
	a.comm.checkedAt, a.comm.reachable, a.comm.secure, a.comm.problems, a.comm.latency = time.Now(), reachable, reachable && len(problems) == 0, problems, lat
	a.comm.version, a.comm.voice = set.About.Version, set.Voice
	nowOK := a.comm.reachable && a.comm.secure
	a.comm.mu.Unlock()
	if first || wasOK != nowOK {
		if nowOK {
			log.Printf("Kommunikation (UCNG %s): einsatzbereit", set.About.Version)
		} else {
			log.Printf("Kommunikation (UCNG): nicht einsatzbereit – %s", strings.Join(problems, "; "))
		}
	}
}

// commTranscriptProblems (ADR-011): Transkripte nicht länger als vereinbart, Spracherkennung nur,
// wenn sie freigegeben ist.
func commTranscriptProblems(s ucngSettings, cfg *CommunicationConfig) []string {
	if s.Phonebook == nil {
		return nil
	}
	var p []string
	if d := s.Phonebook.TranscriptKeepDays; d < 1 || d > cfg.DefaultTranscriptRetentionDays {
		p = append(p, fmt.Sprintf("Transkripte werden %s aufbewahrt – vereinbart sind höchstens %d Tage (TRANSCRIPT_KEEP_DAYS)",
			map[bool]string{true: "unbegrenzt", false: fmt.Sprintf("%d Tage", d)}[d < 1], cfg.DefaultTranscriptRetentionDays))
	}
	if s.Phonebook.STT && !cfg.Features.Transcription {
		p = append(p, "Spracherkennung ist in UCNG eingerichtet (STT_URL), aber nicht freigegeben (communication.features.transcription)")
	}
	return p
}

func errText(code int, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("HTTP %d", code)
}

// commPolicyProblems: Hält UCNG das Sicherheitsprofil des Phonebook-Betriebs ein? (Gate A)
func commPolicyProblems(s ucngSettings, f CommunicationFeatures) []string {
	var p []string
	if s.AppMode != "hosted" || s.Hosted == nil {
		return []string{fmt.Sprintf("UCNG läuft im Modus %q – nötig ist MODE=hosted mit LOCK_GUESTS=none (im Modus simple dürfen Gäste schreiben)", s.AppMode)}
	}
	if s.Hosted.Guests != "none" {
		p = append(p, fmt.Sprintf("Gastzugang aktiv (guests=%s) – LOCK_GUESTS=none setzen", s.Hosted.Guests))
	}
	if s.Hosted.Registration {
		p = append(p, "offene Registrierung aktiv – LOCK_REGISTRATION=1 setzen")
	}
	if s.Hosted.Federation && !f.Federation {
		p = append(p, "Föderation aktiv, aber nicht freigegeben – LOCK_FEDERATION=1 setzen")
	}
	if s.ChanFed != nil && !f.Federation {
		p = append(p, "Kanal-Föderation aktiv, aber nicht freigegeben – LOCK_CHANNEL_FEDERATION=1 setzen")
	}
	if f.Voice && !s.Voice {
		p = append(p, "Sprachkanäle sind konfiguriert, in UCNG aber aus (VOICE=0)")
	}
	// RC (ADR-009): keine Verbindung zu Beacons und GIF-Anbietern – im Modus phonebook fest gesperrt
	if s.Phonebook != nil {
		if !f.Beacon && !contains(s.Phonebook.Locks, "beacon") {
			p = append(p, "Beacon nicht gesperrt (UCNG-Stand vor dem RC?) – LOCK_BEACON=1")
		}
		if !contains(s.Phonebook.Locks, "externalGifs") {
			p = append(p, "GIF-Suche bei GIPHY/Tenor nicht gesperrt (UCNG-Stand vor dem RC?) – LOCK_EXTERNAL_GIFS=1")
		}
	}
	return p
}

// commSSOProblems (PH-02): UCNG muss im Modus phonebook laufen und auf dieses Telefonbuch zeigen –
// sonst gäbe es eine zweite Anmeldung (Passwort, Einladungen) neben dem Telefonbuch.
func commSSOProblems(s ucngSettings, phonebookURL string) []string {
	if s.Phonebook == nil {
		return []string{"UCNG läuft nicht im Modus phonebook (MODE=phonebook) – Anmeldung über das Telefonbuch nicht möglich"}
	}
	want, ok1 := normOrigin(phonebookURL)
	got, ok2 := normOrigin(s.Phonebook.Authorize)
	if phonebookURL != "" && (!ok1 || !ok2 || want != got) {
		return []string{fmt.Sprintf("UCNG meldet ein anderes Telefonbuch (%s) – PHONEBOOK_URL prüfen", s.Phonebook.Authorize)}
	}
	return nil
}

func (a *App) communicationLoop(ctx context.Context) {
	client := commHTTPClient()
	t := time.NewTicker(a.cfg.Communication.ProbeInterval.Duration)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		a.probeCommunication(pctx, client)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// GET /api/addons/communication/status – nur angemeldet; nur wenn das Add-on an ist (sonst 404)
func (a *App) handleCommunicationStatus(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	cfg := a.cfg.Communication
	snap := a.comm.snapshot()
	out := map[string]any{
		"enabled":   true,
		"available": snap.Status == "ok",
		"status":    snap.Status,
		"url":       cfg.PublicBaseURL,
		"features":  cfg.Features,
	}
	if s.IsAdmin {
		out["check"] = snap
		out["internalBaseURL"] = cfg.InternalBaseURL
		out["transcriptRetentionDays"] = cfg.DefaultTranscriptRetentionDays
	} else if snap.Status != "ok" {
		out["message"] = "Kommunikation derzeit nicht erreichbar"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
