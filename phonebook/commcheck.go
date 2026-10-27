package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// telefonbuch commcheck – Routing-Smoke-Test für das Kommunikations-Add-on (PH-01, ADR-004).
// Prüft über den echten Gateway (nginx, Caddy, Traefik …), ob Phonebook und UCNG auf ihren
// Subdomains richtig erreichbar sind: Seiten, API, kein Gastzugang, Sicherheitsprofil,
// WebSocket-Upgrade (beide Richtungen) und – mit Admin-Passwort – SSE ohne Proxy-Puffer.
// Exit-Code 0 = alles bestanden, 1 = mindestens ein Fehler.

type checkReport struct {
	failed, warned int
	out            io.Writer
}

func (c *checkReport) ok(name, detail string) { fmt.Fprintf(c.out, "  ✓ %-38s %s\n", name, detail) }
func (c *checkReport) warn(name, detail string) {
	c.warned++
	fmt.Fprintf(c.out, "  ! %-38s %s\n", name, detail)
}
func (c *checkReport) fail(name, detail string) {
	c.failed++
	fmt.Fprintf(c.out, "  ✗ %-38s %s\n", name, detail)
}

type commChecker struct {
	pb, comm   *url.URL
	client     *http.Client
	dial       func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConf    *tls.Config
	adminPW    string
	sseTimeout time.Duration
	rep        *checkReport
}

func cmdCommCheck(args []string) {
	fsx := flag.NewFlagSet("commcheck", flag.ExitOnError)
	pb := fsx.String("phonebook", "", "Adresse des Telefonbuchs, z. B. https://telefonbuch.firma.local")
	comm := fsx.String("communication", "", "Adresse der Kommunikation (UCNG), z. B. https://kommunikation.firma.local")
	connectTo := fsx.String("connect-to", "", "Optional: alle Verbindungen an host:port statt per DNS (wie curl --connect-to), z. B. 127.0.0.1:8443")
	caFile := fsx.String("ca", "", "Optional: CA-Zertifikat (PEM) für interne Zertifikate")
	pwFile := fsx.String("ucng-admin-password-file", "", "Optional: Datei mit dem UCNG-Admin-Passwort – prüft dann auch SSE ohne Proxy-Puffer")
	fsx.Usage = func() {
		fmt.Fprintln(os.Stderr, "Routing-Smoke-Test für das Kommunikations-Add-on (Subdomain-Betrieb).")
		fmt.Fprintln(os.Stderr, "Aufruf: telefonbuch commcheck -phonebook URL -communication URL [-connect-to host:port] [-ca datei] [-ucng-admin-password-file datei]")
		fsx.PrintDefaults()
	}
	_ = fsx.Parse(args)
	c, err := newCommChecker(*pb, *comm, *connectTo, *caFile, *pwFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fsx.Usage()
		os.Exit(2)
	}
	c.run(context.Background())
	if c.rep.failed > 0 {
		os.Exit(1)
	}
}

func newCommChecker(pb, comm, connectTo, caFile, pwFile string) (*commChecker, error) {
	pu, err := url.Parse(pb)
	if err != nil || pu.Host == "" {
		return nil, errors.New("-phonebook: absolute Adresse nötig")
	}
	cu, err := url.Parse(comm)
	if err != nil || cu.Host == "" {
		return nil, errors.New("-communication: absolute Adresse nötig")
	}
	c := &commChecker{pb: pu, comm: cu, sseTimeout: 3 * time.Second, rep: &checkReport{out: os.Stdout}}
	d := &net.Dialer{Timeout: 5 * time.Second}
	c.dial = d.DialContext
	if connectTo != "" {
		c.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, connectTo)
		}
	}
	c.tlsConf = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("-ca: kein PEM-Zertifikat gefunden")
		}
		c.tlsConf.RootCAs = pool
	}
	if pwFile != "" {
		b, err := os.ReadFile(pwFile)
		if err != nil {
			return nil, err
		}
		c.adminPW = strings.TrimSpace(string(b))
	}
	c.client = &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: c.dial, TLSClientConfig: c.tlsConf, ForceAttemptHTTP2: false},
	}
	return c, nil
}

func (c *commChecker) do(ctx context.Context, method string, base *url.URL, path, body string, hdr map[string]string) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base.String(), "/")+path, rd)
	if err != nil {
		return nil, nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512<<10))
	return res, b, nil
}

func (c *commChecker) run(ctx context.Context) {
	r := c.rep
	fmt.Fprintf(r.out, "Telefonbuch:   %s\nKommunikation: %s\n\n", c.pb, c.comm)
	if po, ok1 := normOrigin(c.pb.String()); ok1 {
		if co, ok2 := normOrigin(c.comm.String()); ok2 && po == co {
			r.fail("Getrennte Herkunft (ADR-004)", "Telefonbuch und Kommunikation haben dieselbe Herkunft – eigene Subdomain nötig")
		} else {
			r.ok("Getrennte Herkunft (ADR-004)", po+" ≠ "+co)
		}
	}
	// 1. Telefonbuch
	if res, b, err := c.do(ctx, "GET", c.pb, "/livez", "", nil); err != nil || res.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok" {
		r.fail("Telefonbuch /livez", describe(res, err))
	} else {
		r.ok("Telefonbuch /livez", "200 ok")
	}
	switch res, _, err := c.do(ctx, "GET", c.pb, "/api/addons/communication/status", "", nil); {
	case err != nil:
		r.fail("Add-on im Telefonbuch", err.Error())
	case res.StatusCode == 401:
		r.ok("Add-on im Telefonbuch", "aktiv (Status nur mit Anmeldung: 401)")
	case res.StatusCode == 404:
		r.warn("Add-on im Telefonbuch", "aus (communication.enabled=false) – Routen fehlen wie vorgesehen")
	default:
		r.fail("Add-on im Telefonbuch", fmt.Sprintf("unerwartet HTTP %d ohne Anmeldung", res.StatusCode))
	}
	// 2. UCNG-Oberfläche und Kopfzeilen
	startCSP := ""
	if res, b, err := c.do(ctx, "GET", c.comm, "/", "", nil); err != nil || res.StatusCode != 200 || !strings.Contains(string(b), "<html") {
		r.fail("Kommunikation: Startseite", describe(res, err))
	} else {
		csp := res.Header.Get("Content-Security-Policy")
		startCSP = csp
		if !strings.Contains(csp, "default-src 'self'") {
			r.fail("Kommunikation: Startseite", "Content-Security-Policy fehlt oder wurde vom Proxy verändert")
		} else {
			r.ok("Kommunikation: Startseite", "200, CSP vorhanden")
		}
		if c.comm.Scheme == "https" && res.Header.Get("Strict-Transport-Security") == "" {
			r.warn("Kommunikation: HSTS", "fehlt (PUBLIC_URL mit https:// setzen)")
		}
	}
	if res, _, err := c.do(ctx, "GET", c.comm, "/healthz", "", nil); err != nil || res.StatusCode != 200 {
		r.fail("Kommunikation: /healthz", describe(res, err))
	} else {
		r.ok("Kommunikation: /healthz", "200")
	}
	// 3. Sicherheitsprofil (dieselbe Prüfung wie im Telefonbuch)
	var set ucngSettings
	if res, b, err := c.do(ctx, "GET", c.comm, "/api/settings", "", nil); err != nil || res.StatusCode != 200 || json.Unmarshal(b, &set) != nil {
		r.fail("Kommunikation: Sicherheitsprofil", "Einstellungen nicht lesbar: "+describe(res, err))
	} else if p := commPolicyProblems(set, CommunicationFeatures{}); len(p) > 0 {
		r.fail("Kommunikation: Sicherheitsprofil", strings.Join(p, "; "))
	} else {
		r.ok("Kommunikation: Sicherheitsprofil", "hosted, Gäste none, keine Registrierung, keine Föderation (UCNG "+set.About.Version+")")
		// PH-02: Anmeldung nur über dieses Telefonbuch (MODE=phonebook, PHONEBOOK_URL)
		if p := commSSOProblems(set, c.pb.String()); len(p) > 0 {
			r.fail("Kommunikation: Anmeldung über Telefonbuch", strings.Join(p, "; "))
		} else {
			r.ok("Kommunikation: Anmeldung über Telefonbuch", "MODE=phonebook → "+set.Phonebook.Authorize)
			// PH-04, ADR-019: Die Kommunikation lässt sich nicht in fremde Seiten einbetten
			if strings.Contains(startCSP, "frame-ancestors 'self'") || strings.Contains(startCSP, "frame-ancestors 'none'") {
				r.ok("Kommunikation: keine Einbettung", "frame-ancestors gesetzt (ADR-019)")
			} else {
				r.fail("Kommunikation: keine Einbettung", "CSP ohne frame-ancestors – UCNG-Stand vor PH-04 oder vom Proxy verändert (Clickjacking, ADR-019)")
			}
		}
	}
	if res, b, err := c.do(ctx, "GET", c.comm, "/.well-known/ucng", "", nil); err == nil && res.StatusCode == 200 {
		var wk struct{ Federation, Channels bool }
		if json.Unmarshal(b, &wk) == nil && (wk.Federation || wk.Channels) {
			r.fail("Kommunikation: /.well-known/ucng", "meldet Föderation an")
		} else {
			r.ok("Kommunikation: /.well-known/ucng", "erreichbar, Föderation aus")
		}
	} else {
		r.warn("Kommunikation: /.well-known/ucng", "nicht erreichbar ("+describe(res, err)+") – PUBLIC_URL gesetzt?")
	}
	// 4. Kein Gastzugang
	for _, x := range []struct{ method, path, body, name string }{
		{"GET", "/api/rooms", "", "Kanalliste"},
		{"GET", "/api/rooms/lobby/messages", "", "Verlauf"},
		{"GET", "/api/rooms/lobby/events", "", "Live-Stream (SSE)"},
		{"GET", "/api/profiles", "", "Profile"},
		{"POST", "/api/session", `{"name":"commcheck"}`, "Gastname"},
		{"POST", "/api/account/register", `{"name":"commcheck"}`, "Registrierung"},
	} {
		res, _, err := c.do(ctx, x.method, c.comm, x.path, x.body, nil)
		switch {
		case err != nil:
			r.fail("Ohne Anmeldung: "+x.name, err.Error())
		case res.StatusCode == 401 || res.StatusCode == 403:
			r.ok("Ohne Anmeldung: "+x.name, fmt.Sprintf("abgewiesen (%d)", res.StatusCode))
		case res.StatusCode == 404 && x.path == "/api/account/register":
			r.ok("Ohne Anmeldung: "+x.name, "nicht vorhanden (404)")
		default:
			r.fail("Ohne Anmeldung: "+x.name, fmt.Sprintf("HTTP %d – Gastzugang!", res.StatusCode))
		}
	}
	// 5. WebSocket über den Proxy (Sprachkanäle)
	if code, err := c.wsProbe(ctx, "/api/voice/ws"); err != nil {
		r.fail("WebSocket /api/voice/ws", err.Error())
	} else if code != 4400 {
		r.fail("WebSocket /api/voice/ws", fmt.Sprintf("Upgrade ok, aber unerwarteter Schließcode %d", code))
	} else {
		r.ok("WebSocket /api/voice/ws", "101 Upgrade, Nachricht hin, Schließcode 4400 zurück")
	}
	// 6. SSE ohne Puffer (nur mit Admin-Passwort)
	if c.adminPW == "" {
		r.warn("SSE ohne Proxy-Puffer", "übersprungen (-ucng-admin-password-file angeben)")
	} else {
		c.sseProbe(ctx)
	}
	fmt.Fprintf(r.out, "\n%d Fehler, %d Hinweise\n", r.failed, r.warned)
}

func describe(res *http.Response, err error) string {
	if err != nil {
		return err.Error()
	}
	if res == nil {
		return "keine Antwort"
	}
	return fmt.Sprintf("HTTP %d", res.StatusCode)
}

// wsProbe: WebSocket-Handshake über den Proxy, eine Textnachricht senden, Schließcode lesen.
// UCNG beantwortet eine Verbindung ohne Anmeldung mit 4400 – das beweist beide Richtungen.
func (c *commChecker) wsProbe(ctx context.Context, path string) (int, error) {
	host := c.comm.Host
	if c.comm.Port() == "" {
		host = net.JoinHostPort(c.comm.Hostname(), map[string]string{"http": "80", "https": "443"}[c.comm.Scheme])
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := c.dial(ctx, "tcp", host)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if c.comm.Scheme == "https" {
		tc := c.tlsConf.Clone()
		tc.ServerName = c.comm.Hostname()
		tc.NextProtos = []string{"http/1.1"}
		t := tls.Client(conn, tc)
		if err := t.HandshakeContext(ctx); err != nil {
			return 0, err
		}
		conn = t
	}
	kb := make([]byte, 16)
	_, _ = rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	origin := strings.TrimRight(c.comm.String(), "/")
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\nUser-Agent: Telefonbuch-commcheck\r\n\r\n",
		path, c.comm.Host, key, origin)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return 0, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		return 0, fmt.Errorf("kein Upgrade: HTTP %d (Proxy leitet Upgrade/Connection nicht weiter?)", res.StatusCode)
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if res.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return 0, errors.New("Sec-WebSocket-Accept falsch")
	}
	// maskierte Textnachricht „{}“
	payload := []byte("{}")
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	frame := []byte{0x81, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := conn.Write(frame); err != nil {
		return 0, err
	}
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(br, h); err != nil {
			return 0, fmt.Errorf("keine Antwort über den WebSocket: %v", err)
		}
		n := int(h[1] & 0x7f)
		switch n {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(br, ext); err != nil {
				return 0, err
			}
			n = int(binary.BigEndian.Uint16(ext))
		case 127:
			return 0, errors.New("unerwartet großer Rahmen")
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			return 0, err
		}
		if h[0]&0x0f == 0x8 { // Close
			if len(body) < 2 {
				return 1005, nil
			}
			return int(binary.BigEndian.Uint16(body[:2])), nil
		}
	}
}

// sseProbe: Admin-Live-Stream öffnen; die ersten Daten müssen sofort ankommen (kein Proxy-Puffer).
func (c *commChecker) sseProbe(ctx context.Context) {
	r := c.rep
	res, b, err := c.do(ctx, "POST", c.comm, "/api/admin/login", fmt.Sprintf(`{"password":%q}`, c.adminPW), nil)
	var login struct{ Token string }
	if err != nil || res.StatusCode != 200 || json.Unmarshal(b, &login) != nil || login.Token == "" {
		r.fail("SSE ohne Proxy-Puffer", "Admin-Anmeldung fehlgeschlagen: "+describe(res, err))
		return
	}
	defer func() {
		_, _, _ = c.do(context.Background(), "POST", c.comm, "/api/admin/logout", "", map[string]string{"Authorization": "Bearer " + login.Token})
	}()
	room := "lobby"
	if res, b, err := c.do(ctx, "GET", c.comm, "/api/admin/rooms", "", map[string]string{"Authorization": "Bearer " + login.Token}); err == nil && res.StatusCode == 200 {
		var rooms []struct{ Name string }
		if json.Unmarshal(b, &rooms) == nil && len(rooms) > 0 {
			room = rooms[0].Name
		}
	}
	sctx, cancel := context.WithTimeout(ctx, c.sseTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", strings.TrimRight(c.comm.String(), "/")+"/api/admin/rooms/"+url.PathEscape(room)+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	req.Header.Set("Accept", "text/event-stream")
	t0 := time.Now()
	sc := &http.Client{Transport: c.client.Transport, CheckRedirect: c.client.CheckRedirect} // ohne Gesamt-Timeout
	sres, err := sc.Do(req)
	if err != nil {
		r.fail("SSE ohne Proxy-Puffer", "keine Antwort binnen "+c.sseTimeout.String()+" – Proxy puffert? ("+err.Error()+")")
		return
	}
	defer sres.Body.Close()
	if sres.StatusCode != 200 || !strings.HasPrefix(sres.Header.Get("Content-Type"), "text/event-stream") {
		r.fail("SSE ohne Proxy-Puffer", fmt.Sprintf("HTTP %d, Content-Type %q", sres.StatusCode, sres.Header.Get("Content-Type")))
		return
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(sres.Body, buf); err != nil {
		r.fail("SSE ohne Proxy-Puffer", "Kopfzeilen kamen, aber keine Daten binnen "+c.sseTimeout.String()+" – proxy_buffering off?")
		return
	}
	r.ok("SSE ohne Proxy-Puffer", fmt.Sprintf("erste Daten nach %d ms", time.Since(t0).Milliseconds()))
}
