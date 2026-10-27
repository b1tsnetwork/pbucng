package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client-Protokoll: Start/Stop des Windows-Symbols. MySQL: Tabelle client_events.
// JSON-Betrieb: eigene Datei „….clients.jsonl“ (eine Zeile je Ereignis, wird nur angehängt).

// ClientEvent: ein Start oder Stop eines Clients
type ClientEvent struct {
	ID        int64      `json:"id"`
	At        time.Time  `json:"at"`                  // Eingang beim Server (UTC)
	ClientAt  *time.Time `json:"clientAt,omitempty"`  // Zeitstempel laut Client
	Event     string     `json:"event"`               // start, stop
	Reason    string     `json:"reason,omitempty"`    // z. B. beenden, abmelden, herunterfahren
	Hostname  string     `json:"hostname"`            // Rechnername
	Username  string     `json:"username"`            // DOMÄNE\benutzer laut Client
	ClientIPs string     `json:"clientIps,omitempty"` // vom Client gemeldete Adressen, kommagetrennt
	RemoteIP  string     `json:"remoteIp"`            // Absender laut Server (hinter Proxy: X-Forwarded-For)
	Version   string     `json:"version,omitempty"`   // Version des Windows-Symbols
}

// ClientEventFilter: Suche (Rechner, Benutzer, IP) bzw. genau ein Rechner
type ClientEventFilter struct {
	Query string
	Host  string
	Limit int
}

func (f ClientEventFilter) match(e *ClientEvent) bool {
	if f.Host != "" && !strings.EqualFold(e.Hostname, f.Host) {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		hay := strings.ToLower(e.Hostname + "\x00" + e.Username + "\x00" + e.ClientIPs + "\x00" + e.RemoteIP)
		return strings.Contains(hay, q)
	}
	return true
}

func clientsPathFor(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		return path[:len(path)-5] + ".clients.jsonl"
	}
	return path + ".clients.jsonl"
}

// ---------- JSON-Betrieb ----------

const jsonClientEventsMax = 200000 // darüber werden die ältesten verworfen

type clientFile struct {
	mu     sync.Mutex
	path   string
	loaded bool
	events []ClientEvent // älteste zuerst
	next   int64
}

func (c *clientFile) load() error {
	if c.loaded {
		return nil
	}
	c.next = 1
	f, err := os.Open(c.path)
	if os.IsNotExist(err) {
		c.loaded = true
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	bad := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e ClientEvent
		if json.Unmarshal(line, &e) != nil || e.ID <= 0 {
			bad++
			continue
		}
		c.events = append(c.events, e)
		c.next = max(c.next, e.ID+1)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if bad > 0 {
		log.Printf("Client-Protokoll %s: %d unlesbare Zeile(n) übersprungen", c.path, bad)
	}
	sort.Slice(c.events, func(i, j int) bool { return c.events[i].ID < c.events[j].ID })
	c.loaded = true
	return nil
}

func (c *clientFile) rewrite() error {
	var buf bytes.Buffer
	for i := range c.events {
		b, _ := json.Marshal(&c.events[i])
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(c.path, buf.Bytes())
}

func (s *jsonStore) AddClientEvent(ctx context.Context, e ClientEvent) error {
	c := s.clients
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(); err != nil {
		return err
	}
	e.ID = c.next
	c.next++
	c.events = append(c.events, e)
	if len(c.events) > jsonClientEventsMax+jsonClientEventsMax/10 {
		c.events = append([]ClientEvent(nil), c.events[len(c.events)-jsonClientEventsMax:]...)
		return c.rewrite()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(&e)
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *jsonStore) ListClientEvents(ctx context.Context, f ClientEventFilter) ([]ClientEvent, error) {
	c := s.clients
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(); err != nil {
		return nil, err
	}
	out := []ClientEvent{}
	for i := len(c.events) - 1; i >= 0; i-- {
		if f.match(&c.events[i]) {
			out = append(out, c.events[i])
			if f.Limit > 0 && len(out) >= f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (s *jsonStore) LatestClientEvents(ctx context.Context) ([]ClientEvent, error) {
	c := s.clients
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(); err != nil {
		return nil, err
	}
	last := map[string]ClientEvent{}
	for _, e := range c.events {
		last[strings.ToLower(e.Hostname)] = e
	}
	out := make([]ClientEvent, 0, len(last))
	for _, e := range last {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Hostname) < strings.ToLower(out[j].Hostname) })
	return out, nil
}

func (s *jsonStore) PruneClientEvents(ctx context.Context, before time.Time) (int, error) {
	c := s.clients
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.load(); err != nil {
		return 0, err
	}
	keep := c.events[:0:0]
	for _, e := range c.events {
		if !e.At.Before(before) {
			keep = append(keep, e)
		}
	}
	n := len(c.events) - len(keep)
	if n == 0 {
		return 0, nil
	}
	c.events = keep
	return n, c.rewrite()
}

func (r *readOnlyStore) AddClientEvent(context.Context, ClientEvent) error { return ErrReadOnly }
func (r *readOnlyStore) PruneClientEvents(context.Context, time.Time) (int, error) {
	return 0, ErrReadOnly
}

// ---------- MySQL ----------

const clientCols = `id, at, client_at, event, reason, hostname, username, client_ips, remote_ip, version`

func scanClientEvents(rows *sql.Rows) ([]ClientEvent, error) {
	defer rows.Close()
	out := []ClientEvent{}
	for rows.Next() {
		var e ClientEvent
		var ca sql.NullTime
		if err := rows.Scan(&e.ID, &e.At, &ca, &e.Event, &e.Reason, &e.Hostname, &e.Username, &e.ClientIPs, &e.RemoteIP, &e.Version); err != nil {
			return nil, err
		}
		if ca.Valid {
			t := ca.Time
			e.ClientAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func (m *mysqlStore) AddClientEvent(ctx context.Context, e ClientEvent) error {
	var ca any
	if e.ClientAt != nil {
		ca = e.ClientAt.UTC()
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO client_events (at, client_at, event, reason, hostname, username, client_ips, remote_ip, version) VALUES (?,?,?,?,?,?,?,?,?)`,
		e.At.UTC(), ca, e.Event, e.Reason, e.Hostname, e.Username, e.ClientIPs, e.RemoteIP, e.Version)
	return err
}

func (m *mysqlStore) ListClientEvents(ctx context.Context, f ClientEventFilter) ([]ClientEvent, error) {
	q := `SELECT ` + clientCols + ` FROM client_events WHERE 1=1`
	var args []any
	if f.Host != "" {
		q += ` AND hostname = ?`
		args = append(args, f.Host)
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		l := likeEscape(s)
		q += ` AND (hostname LIKE ? OR username LIKE ? OR client_ips LIKE ? OR remote_ip LIKE ?)`
		args = append(args, l, l, l, l)
	}
	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanClientEvents(rows)
}

func (m *mysqlStore) LatestClientEvents(ctx context.Context) ([]ClientEvent, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+clientCols+` FROM client_events WHERE id IN (SELECT MAX(id) FROM client_events GROUP BY hostname) ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	return scanClientEvents(rows)
}

func (m *mysqlStore) PruneClientEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM client_events WHERE at < ?`, before.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
