// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Schutz gegen Anfragen ins eigene Netz (SSRF) für Downloads, die der Admin-Bereich auslöst
// (Emotes/GIFs per Adresse) und für Bot-Webhooks. Im Modus phonebook (RC, Sicherheitsprüfung PH-11)
// gehören Admin-Rechte auch Personen, die das Telefonbuch zuordnet – ein Admin soll darüber
// nicht das interne Netz abfragen können (Datenbank, Telefonbuch-Schnittstellen,
// Cloud-Metadaten).
//
//   - immer gesperrt: Loopback, Link-Local (u. a. 169.254.169.254), unspezifizierte und
//     Multicast-Adressen
//   - private Netze (RFC 1918, ULA, CGNAT): nur wenn erlaubt (Webhooks: ja – Bots laufen meist
//     intern; Downloads: nur mit OUTBOUND_ALLOW_PRIVATE=1)
//
// Geprüft wird die tatsächlich verbundene Adresse (nach der Namensauflösung, gegen DNS-Rebinding).
// Läuft die Anfrage über einen Proxy (HTTPS_PROXY), prüft UCNG vorher die Adressen des Ziels.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

var ErrBlockedAddr = errors.New("Diese Adresse ist aus Sicherheitsgründen gesperrt (internes Netz)")

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func blockedAddr(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if !allowPrivate && (ip.IsPrivate() || cgnat.Contains(ip)) {
		return true
	}
	return false
}

// guardedClient: HTTP-Client, der gesperrte Adressen nicht erreicht.
func guardedClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return ErrBlockedAddr
		}
		if blockedAddr(ap.Addr(), allowPrivate) {
			return fmt.Errorf("%w: %s", ErrBlockedAddr, ap.Addr())
		}
		return nil
	}}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = func(r *http.Request) (*url.URL, error) { return http.ProxyFromEnvironment(r) }
	tr := &guardTransport{base: base, allowPrivate: allowPrivate}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ctx.Value(proxyDialKey{}) != nil { // Verbindung zum Proxy selbst – das Ziel wurde vorher geprüft
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("zu viele Weiterleitungen")
		}
		return nil
	}}
}

type proxyDialKey struct{}

// guardTransport prüft bei Proxy-Betrieb vorab die Adressen des Ziels.
type guardTransport struct {
	base         *http.Transport
	allowPrivate bool
}

func (t *guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if u, _ := http.ProxyFromEnvironment(r); u != nil {
		host := r.URL.Hostname()
		ips, err := net.DefaultResolver.LookupNetIP(r.Context(), "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if blockedAddr(ip, t.allowPrivate) {
				return nil, fmt.Errorf("%w: %s", ErrBlockedAddr, ip)
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), proxyDialKey{}, true))
	}
	return t.base.RoundTrip(r)
}
