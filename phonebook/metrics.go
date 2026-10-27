package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Betrieb:
//   /livez   – Prozess läuft (immer 200, ohne Datenbank)
//   /readyz  – bereit? Datenhaltung erreichbar; JSON, Details nur für erlaubte Netze bzw. mit Token.
//              200 = ok oder degraded (Rückfall: nur lesend), 503 = down
//   /healthz – wie bisher (Docker-HEALTHCHECK)
//   /metrics – Prometheus-Textformat, ohne Fremdbibliothek

var durBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type reqKey struct{ route, method, code string }

type durStat struct {
	sum   float64
	count uint64
}

type metrics struct {
	start    time.Time
	inflight atomic.Int64

	mu        sync.Mutex
	req       map[reqKey]uint64
	routeDur  map[string]*durStat
	hist      []uint64 // kumulativ je Grenze in durBuckets (+Inf = histCount)
	histSum   float64
	histCount uint64
	logins    map[string]uint64
	clientEv  map[string]uint64

	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheText []byte
}

func newMetrics() *metrics {
	return &metrics{start: time.Now(), req: map[reqKey]uint64{}, routeDur: map[string]*durStat{},
		hist: make([]uint64, len(durBuckets)), logins: map[string]uint64{}, clientEv: map[string]uint64{}}
}

func (m *metrics) login(result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.logins[result]++
	m.mu.Unlock()
}

func (m *metrics) clientEvent(result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.clientEv[result]++
	m.mu.Unlock()
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	if s.code == 0 {
		s.code = c
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// instrument zählt Anfragen je Route (Muster des ServeMux, z. B. „GET /api/employees/{id}“)
func (a *App) instrument(next http.Handler) http.Handler {
	m := a.m
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inflight.Add(1)
		t0 := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			m.inflight.Add(-1)
			d := time.Since(t0).Seconds()
			code := rec.code
			if code == 0 {
				code = http.StatusOK
			}
			route := r.Pattern
			if route == "" {
				route = "unbekannt"
			} else if i := strings.IndexByte(route, ' '); i >= 0 {
				route = route[i+1:] // Methode steht im eigenen Label
			}
			m.mu.Lock()
			m.req[reqKey{route, r.Method, fmt.Sprintf("%dxx", code/100)}]++
			ds := m.routeDur[route]
			if ds == nil {
				ds = &durStat{}
				m.routeDur[route] = ds
			}
			ds.sum += d
			ds.count++
			for i, b := range durBuckets {
				if d <= b {
					m.hist[i]++
				}
			}
			m.histSum += d
			m.histCount++
			m.mu.Unlock()
		}()
		next.ServeHTTP(rec, r)
	})
}

// monitoringAllowed: Token (falls konfiguriert) oder Absender aus monitoring.allow
func (a *App) monitoringAllowed(r *http.Request) bool {
	if tok := a.cfg.Monitoring.Token; tok != "" {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(tok)) == 1
	}
	ip := net.ParseIP(a.clientIP(r))
	if ip == nil {
		return false
	}
	for _, n := range a.cfg.metricsAllow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *App) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok"))
}

type checkResult struct {
	Status    string  `json:"status"` // ok, degraded, down, unknown
	Mode      string  `json:"mode,omitempty"`
	LatencyMs float64 `json:"latencyMs,omitempty"`
	Message   string  `json:"message,omitempty"`
	At        string  `json:"at,omitempty"`
}

func (a *App) pingStore(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	t0 := time.Now()
	err := a.st().Ping(ctx)
	return time.Since(t0), err
}

func (a *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := map[string]checkResult{}
	status := "ok"
	lat, err := a.pingStore(ctx)
	st := checkResult{Status: "ok", Mode: a.cfg.Storage.Mode, LatencyMs: float64(lat.Microseconds()) / 1000}
	if err != nil {
		st.Status, st.Message, status = "down", "Datenhaltung nicht erreichbar", "down"
	} else if a.offline.Load() {
		st.Status, st.Message, status = "degraded", "Rückfall auf JSON-Kopie – nur lesend", "degraded"
	}
	checks["storage"] = st
	if a.cfg.ldapEnabled() {
		c := checkResult{Status: "unknown"}
		if runs, err := a.st().ListSyncRuns(ctx, 1); err == nil && len(runs) > 0 {
			c.At = runs[0].StartedAt.Format(time.RFC3339)
			switch runs[0].Status {
			case "ok":
				c.Status = "ok"
			case "running":
				c.Status = "ok"
				c.Message = "AD-Abgleich läuft"
			default:
				c.Status, c.Message = "degraded", "letzter AD-Abgleich fehlgeschlagen"
				if status == "ok" {
					status = "degraded"
				}
			}
		}
		checks["adSync"] = c
	}
	if a.cfg.Communication.Enabled { // Kommunikation: höchstens „degraded“ – das Telefonbuch bleibt nutzbar
		cs := a.comm.snapshot()
		c := checkResult{Status: "ok", At: cs.CheckedAt, LatencyMs: cs.LatencyMs}
		if cs.Status != "ok" {
			c.Status, c.Message = "degraded", "Kommunikation: "+cs.Status
			if len(cs.Problems) > 0 {
				c.Message += " – " + strings.Join(cs.Problems, "; ")
			}
			if status == "ok" {
				status = "degraded"
			}
		}
		checks["communication"] = c
	}
	code := http.StatusOK
	if status == "down" {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{"status": status}
	if a.monitoringAllowed(r) {
		out["version"] = version
		out["uptimeSeconds"] = int64(time.Since(a.startTime()).Seconds())
		out["checks"] = checks
	}
	writeJSON(w, code, out)
}

func (a *App) startTime() time.Time {
	if a.m != nil {
		return a.m.start
	}
	return time.Now()
}

// ---------- /metrics ----------

func promEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

type promWriter struct{ bytes.Buffer }

func (p *promWriter) head(name, typ, help string) {
	fmt.Fprintf(p, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *promWriter) val(name string, labels []string, v float64) {
	p.WriteString(name)
	if len(labels) > 0 {
		p.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				p.WriteByte(',')
			}
			fmt.Fprintf(p, `%s="%s"`, labels[i], promEsc(labels[i+1]))
		}
		p.WriteByte('}')
	}
	fmt.Fprintf(p, " %s\n", fmtFloat(v))
}

func fmtFloat(v float64) string {
	if v == float64(int64(v)) && v < 1e15 && v > -1e15 {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.metricsEnabled() {
		http.NotFound(w, r)
		return
	}
	if !a.monitoringAllowed(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		http.Error(w, "nicht erlaubt", http.StatusForbidden)
		return
	}
	var p promWriter
	m := a.m
	if m == nil {
		m = newMetrics()
	}
	p.head("tb_build_info", "gauge", "Version des Telefonbuchs")
	p.val("tb_build_info", []string{"version", version, "goversion", runtime.Version()}, 1)
	p.head("tb_process_start_time_seconds", "gauge", "Startzeit (Unix-Sekunden)")
	p.val("tb_process_start_time_seconds", nil, float64(m.start.Unix()))
	p.head("tb_http_requests_in_flight", "gauge", "Gerade bearbeitete Anfragen")
	p.val("tb_http_requests_in_flight", nil, float64(m.inflight.Load()))

	m.mu.Lock()
	keys := make([]reqKey, 0, len(m.req))
	for k := range m.req {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].code < keys[j].code
	})
	p.head("tb_http_requests_total", "counter", "HTTP-Anfragen je Route, Methode und Statusklasse")
	for _, k := range keys {
		p.val("tb_http_requests_total", []string{"route", k.route, "method", k.method, "code", k.code}, float64(m.req[k]))
	}
	routes := make([]string, 0, len(m.routeDur))
	for k := range m.routeDur {
		routes = append(routes, k)
	}
	sort.Strings(routes)
	p.head("tb_http_route_duration_seconds", "summary", "Bearbeitungszeit je Route (Summe/Anzahl)")
	for _, rt := range routes {
		d := m.routeDur[rt]
		p.val("tb_http_route_duration_seconds_sum", []string{"route", rt}, d.sum)
		p.val("tb_http_route_duration_seconds_count", []string{"route", rt}, float64(d.count))
	}
	p.head("tb_http_request_duration_seconds", "histogram", "Bearbeitungszeit aller Anfragen")
	for i, b := range durBuckets {
		p.val("tb_http_request_duration_seconds_bucket", []string{"le", fmtFloat(b)}, float64(m.hist[i]))
	}
	p.val("tb_http_request_duration_seconds_bucket", []string{"le", "+Inf"}, float64(m.histCount))
	p.val("tb_http_request_duration_seconds_sum", nil, m.histSum)
	p.val("tb_http_request_duration_seconds_count", nil, float64(m.histCount))
	p.head("tb_logins_total", "counter", "Anmeldeversuche (ok, fehlgeschlagen, gesperrt)")
	for _, k := range []string{"ok", "failed", "blocked"} {
		p.val("tb_logins_total", []string{"result", k}, float64(m.logins[k]))
	}
	p.head("tb_client_events_total", "counter", "Meldungen des Windows-Symbols (start, stop, abgelehnt)")
	for _, k := range []string{"start", "stop", "rejected"} {
		p.val("tb_client_events_total", []string{"event", k}, float64(m.clientEv[k]))
	}
	m.mu.Unlock()

	// Datenhaltung (bei jedem Abruf)
	lat, err := a.pingStore(r.Context())
	p.head("tb_storage_up", "gauge", "Datenhaltung erreichbar (1/0)")
	p.val("tb_storage_up", nil, b2f(err == nil))
	p.head("tb_storage_ping_seconds", "gauge", "Antwortzeit der Datenhaltung")
	p.val("tb_storage_ping_seconds", nil, lat.Seconds())
	p.head("tb_storage_offline", "gauge", "Rückfall auf die JSON-Kopie aktiv (nur lesend)")
	p.val("tb_storage_offline", nil, b2f(a.offline.Load()))
	if a.cfg.Communication.Enabled {
		cs := a.comm.snapshot()
		p.head("tb_communication_up", "gauge", "Kommunikations-Add-on (UCNG) erreichbar (1/0)")
		p.val("tb_communication_up", nil, b2f(cs.Reachable))
		p.head("tb_communication_secure", "gauge", "UCNG hält das Sicherheitsprofil ein: keine Gäste, keine offene Registrierung, keine Föderation (1/0)")
		p.val("tb_communication_secure", nil, b2f(cs.Secure))
		p.head("tb_communication_probe_seconds", "gauge", "Dauer der letzten Prüfung von UCNG")
		p.val("tb_communication_probe_seconds", nil, cs.LatencyMs/1000)
	}
	p.head("tb_storage_info", "gauge", "Art der Datenhaltung")
	p.val("tb_storage_info", []string{"mode", a.cfg.Storage.Mode}, 1)

	// Bestand (zwischengespeichert, höchstens alle 15 s neu)
	p.Write(a.inventoryMetrics(r.Context(), m))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p.head("go_goroutines", "gauge", "Anzahl Goroutinen")
	p.val("go_goroutines", nil, float64(runtime.NumGoroutine()))
	p.head("go_memstats_heap_alloc_bytes", "gauge", "Belegter Heap")
	p.val("go_memstats_heap_alloc_bytes", nil, float64(ms.HeapAlloc))
	p.head("go_memstats_sys_bytes", "gauge", "Vom System bezogener Speicher")
	p.val("go_memstats_sys_bytes", nil, float64(ms.Sys))
	p.head("go_gc_cycles_total", "counter", "Abgeschlossene GC-Zyklen")
	p.val("go_gc_cycles_total", nil, float64(ms.NumGC))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(p.Bytes())
}

func (a *App) inventoryMetrics(ctx context.Context, m *metrics) []byte {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if m.cacheText != nil && time.Since(m.cacheAt) < 15*time.Second {
		return m.cacheText
	}
	var p promWriter
	st := a.st()
	if emps, err := st.ListEmployees(ctx, true); err == nil {
		var act, hid, inact float64
		for _, e := range emps {
			switch {
			case !e.Active:
				inact++
			case e.Hidden:
				hid++
			default:
				act++
			}
		}
		p.head("tb_employees", "gauge", "Einträge im Telefonbuch")
		p.val("tb_employees", []string{"state", "active"}, act)
		p.val("tb_employees", []string{"state", "hidden"}, hid)
		p.val("tb_employees", []string{"state", "inactive"}, inact)
	}
	if tags, err := st.ListTags(ctx); err == nil {
		p.head("tb_tags", "gauge", "Schlagworte")
		p.val("tb_tags", nil, float64(len(tags)))
	}
	if units, err := st.ListOrgUnits(ctx); err == nil {
		p.head("tb_org_units", "gauge", "Organisationseinheiten")
		p.val("tb_org_units", nil, float64(len(units)))
	}
	if tasks, err := st.ListTasks(ctx); err == nil {
		p.head("tb_tasks", "gauge", "Aufgaben im Zuständigkeitsfinder")
		p.val("tb_tasks", nil, float64(len(tasks)))
	}
	if n, err := st.CountReports(ctx, "open", ""); err == nil {
		p.head("tb_reports_open", "gauge", "Offene Meldungen")
		p.val("tb_reports_open", nil, float64(n))
	}
	if runs, err := st.ListSyncRuns(ctx, 1); err == nil && len(runs) > 0 {
		x := runs[0]
		p.head("tb_adsync_last_run_timestamp_seconds", "gauge", "Beginn des letzten AD-Abgleichs")
		p.val("tb_adsync_last_run_timestamp_seconds", nil, float64(x.StartedAt.Unix()))
		p.head("tb_adsync_last_success", "gauge", "Letzter AD-Abgleich erfolgreich (1/0)")
		p.val("tb_adsync_last_success", nil, b2f(x.Status == "ok"))
		if x.FinishedAt != nil {
			p.head("tb_adsync_last_duration_seconds", "gauge", "Dauer des letzten AD-Abgleichs")
			p.val("tb_adsync_last_duration_seconds", nil, x.FinishedAt.Sub(x.StartedAt).Seconds())
		}
		p.head("tb_adsync_last_seen", "gauge", "Im letzten AD-Abgleich gefundene Konten")
		p.val("tb_adsync_last_seen", nil, float64(x.Seen))
	}
	if a.cfg.ClientLog.Enabled {
		if last, err := st.LatestClientEvents(ctx); err == nil {
			running := 0
			for _, e := range last {
				if e.Event == "start" {
					running++
				}
			}
			p.head("tb_clients_known", "gauge", "Rechner im Client-Protokoll")
			p.val("tb_clients_known", nil, float64(len(last)))
			p.head("tb_clients_running", "gauge", "Rechner, deren letztes Ereignis ein Start ist")
			p.val("tb_clients_running", nil, float64(running))
		}
	}
	m.cacheText, m.cacheAt = p.Bytes(), time.Now()
	return m.cacheText
}
