package main

import (
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client-Protokoll (rein administrative Datenvorhaltung): Das Windows-Symbol meldet beim Start und
// beim Beenden Rechnername, IP-Adressen, Benutzername, Zeitstempel und das Ereignis (start/stop).
// Sichtbar nur für Admins, automatische Löschung nach clientLog.retentionDays.

// ---------- einfache Ratenbegrenzung je Schlüssel (feste Fenster) ----------

type rateLimit struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*rlEntry
}

type rlEntry struct {
	start time.Time
	n     int
}

func newRateLimit(max int, window time.Duration) *rateLimit {
	return &rateLimit{max: max, window: window, hits: map[string]*rlEntry{}}
}

func (l *rateLimit) allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 50000 { // Speicher begrenzen
		for k, e := range l.hits {
			if now.Sub(e.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	e := l.hits[key]
	if e == nil || now.Sub(e.start) > l.window {
		l.hits[key] = &rlEntry{start: now, n: 1}
		return true
	}
	e.n++
	return e.n <= l.max
}

// ---------- Annahme ----------

// flexStrings nimmt eine Liste oder einen einzelnen String (PowerShell macht aus 1-Element-Arrays gern Strings)
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var list []string
	if json.Unmarshal(b, &list) == nil {
		*f = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*f = strings.FieldsFunc(one, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	return nil
}

var (
	reHostname   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reClientUser = regexp.MustCompile(`^[\p{L}\p{N} ._@$\\-]{1,128}$`)
	reClientVer  = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,20}$`)
)

var clientReasons = map[string]bool{"": true, "beenden": true, "abmelden": true, "herunterfahren": true, "fehler": true}

type clientEventInput struct {
	Event      string      `json:"event"`
	Reason     string      `json:"reason"`
	Hostname   string      `json:"hostname"`
	Username   string      `json:"username"`
	IPs        flexStrings `json:"ips"`
	ClientTime string      `json:"clientTime"`
	Version    string      `json:"version"`
}

func (in *clientEventInput) toEvent(now time.Time, remote string) (ClientEvent, error) {
	e := ClientEvent{At: now.UTC(), RemoteIP: remote}
	switch strings.ToLower(strings.TrimSpace(in.Event)) {
	case "start":
		e.Event = "start"
	case "stop":
		e.Event = "stop"
	default:
		return e, errors.New("event muss start oder stop sein")
	}
	e.Reason = strings.ToLower(strings.TrimSpace(in.Reason))
	if !clientReasons[e.Reason] {
		e.Reason = ""
	}
	e.Hostname = strings.TrimSpace(in.Hostname)
	if !reHostname.MatchString(e.Hostname) {
		return e, errors.New("hostname ungültig")
	}
	e.Hostname = strings.ToUpper(e.Hostname)
	e.Username = strings.TrimSpace(in.Username)
	if e.Username != "" && !reClientUser.MatchString(e.Username) {
		return e, errors.New("username ungültig")
	}
	var ips []string
	seen := map[string]bool{}
	for _, s := range in.IPs {
		ip := net.ParseIP(strings.TrimSpace(strings.SplitN(s, "%", 2)[0])) // Zonen-ID (fe80::1%12) weg
		if ip == nil || ip.IsLoopback() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		ips = append(ips, ip.String())
		if len(ips) >= 8 {
			break
		}
	}
	e.ClientIPs = strings.Join(ips, ", ")
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(in.ClientTime)); err == nil {
		t = t.UTC()
		e.ClientAt = &t
	}
	if v := strings.TrimSpace(in.Version); v != "" && reClientVer.MatchString(v) {
		e.Version = v
	}
	return e, nil
}

// POST /api/client/event – ohne Anmeldung (das Windows-Symbol hat keine Sitzung)
func (a *App) handleClientEvent(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ClientLog.Enabled {
		jsonError(w, http.StatusNotFound, "Client-Protokoll ist abgeschaltet")
		return
	}
	reject := func(code int, msg string) {
		a.m.clientEvent("rejected")
		jsonError(w, code, msg)
	}
	// Browser von fremden Seiten fernhalten: nur JSON (erzwingt Preflight), kein fremder Origin
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		reject(http.StatusUnsupportedMediaType, "Content-Type application/json erwartet")
		return
	}
	if r.Header.Get("Origin") != "" && !a.sameOrigin(r) {
		reject(http.StatusForbidden, "Fremder Ursprung")
		return
	}
	if key := a.cfg.ClientLog.Key; key != "" {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TB-Client-Key")), []byte(key)) != 1 {
			reject(http.StatusForbidden, "Client-Schlüssel fehlt oder ist falsch")
			return
		}
	}
	remote := a.clientIP(r)
	if !a.clientRL.allow(remote) {
		reject(http.StatusTooManyRequests, "Zu viele Meldungen")
		return
	}
	var in clientEventInput
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reject(http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	e, err := in.toEvent(time.Now(), remote)
	if err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st().AddClientEvent(r.Context(), e); err != nil {
		a.m.clientEvent("rejected")
		storeErr(w, err, "", "")
		return
	}
	a.m.clientEvent(e.Event)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Anzeige für Admins ----------

type clientHost struct {
	ClientEvent
	Running bool `json:"running"` // letztes Ereignis ist ein Start
}

func (a *App) handleListClients(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	f := ClientEventFilter{Query: strings.TrimSpace(q.Get("q")), Host: strings.TrimSpace(q.Get("host")), Limit: limit}
	if len(f.Query) > 100 || len(f.Host) > 64 {
		jsonError(w, http.StatusBadRequest, "Suchbegriff zu lang")
		return
	}
	events, err := a.st().ListClientEvents(ctx, f)
	if err != nil {
		dbFail(w, err)
		return
	}
	latest, err := a.st().LatestClientEvents(ctx)
	if err != nil {
		dbFail(w, err)
		return
	}
	hosts := make([]clientHost, 0, len(latest))
	for _, e := range latest {
		if f.Query != "" && !f.match(&e) {
			continue
		}
		hosts = append(hosts, clientHost{ClientEvent: e, Running: e.Event == "start"})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": a.cfg.ClientLog.Enabled, "retentionDays": a.cfg.ClientLog.RetentionDays, "keyRequired": a.cfg.ClientLog.Key != "",
		"hosts": hosts, "events": events, "limit": limit,
	})
}

func (a *App) handleClientsCSV(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	f := ClientEventFilter{Query: strings.TrimSpace(r.URL.Query().Get("q")), Host: strings.TrimSpace(r.URL.Query().Get("host"))}
	if len(f.Query) > 100 || len(f.Host) > 64 {
		jsonError(w, http.StatusBadRequest, "Suchbegriff zu lang")
		return
	}
	events, err := a.st().ListClientEvents(r.Context(), f)
	if err != nil {
		dbFail(w, err)
		return
	}
	loc := a.cfg.loc
	if loc == nil {
		loc = time.Local
	}
	a.audit(r, s.Username, "clients_export", f.Query, fmt.Sprintf("%d Ereignis(se)", len(events)))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="client-protokoll.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // BOM für Excel
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"Eingang", "Zeit Client", "Ereignis", "Grund", "Rechner", "Benutzer", "IP (Client)", "IP (Absender)", "Version"})
	for _, e := range events {
		ca := ""
		if e.ClientAt != nil {
			ca = e.ClientAt.In(loc).Format("02.01.2006 15:04:05")
		}
		_ = cw.Write([]string{e.At.In(loc).Format("02.01.2006 15:04:05"), ca, e.Event, e.Reason, csvSafe(e.Hostname), csvSafe(e.Username), e.ClientIPs, e.RemoteIP, e.Version})
	}
	cw.Flush()
}

// csvSafe verhindert Formel-Injektion in Tabellenkalkulationen
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// clientLogGC löscht Einträge nach Ablauf der Aufbewahrungsfrist (stündlich)
func (a *App) clientLogGC(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if !a.offline.Load() {
			before := time.Now().Add(-time.Duration(a.cfg.ClientLog.RetentionDays) * 24 * time.Hour)
			if n, err := a.st().PruneClientEvents(ctx, before); err != nil {
				log.Printf("Client-Protokoll bereinigen: %v", err)
			} else if n > 0 {
				log.Printf("Client-Protokoll: %d Einträge älter als %d Tage gelöscht", n, a.cfg.ClientLog.RetentionDays)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
