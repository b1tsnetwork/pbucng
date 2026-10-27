// devgateway: kleiner Host-basierter Reverse-Proxy für Entwicklung und Tests der Subdomain-Variante
// (ADR-004). Nicht für den Produktivbetrieb – dort nginx, Caddy oder Traefik (siehe deploy/gateway).
//
//	go run ./tools/devgateway -listen 127.0.0.1:8180 \
//	   -route telefonbuch.example.test=http://127.0.0.1:8080 \
//	   -route kommunikation.example.test=http://127.0.0.1:8095
//
// Zum Testen des Prüfwerkzeugs lassen sich typische Proxy-Fehler nachstellen:
// -buffer (Antworten puffern wie nginx ohne „proxy_buffering off“) und -strip-upgrade
// (WebSocket-Kopfzeilen nicht weiterreichen).
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type routes map[string]*url.URL

func (r routes) String() string { return "" }
func (r routes) Set(v string) error {
	host, target, ok := strings.Cut(v, "=")
	u, err := url.Parse(target)
	if !ok || err != nil || u.Host == "" {
		return &url.Error{Op: "route", URL: v, Err: err}
	}
	r[strings.ToLower(host)] = u
	return nil
}

// bufferWriter verbirgt Flush – der Server gibt dann erst bei vollem Puffer oder Ende aus.
type bufferWriter struct{ w http.ResponseWriter }

func (b bufferWriter) Header() http.Header         { return b.w.Header() }
func (b bufferWriter) Write(p []byte) (int, error) { return b.w.Write(p) }
func (b bufferWriter) WriteHeader(c int)           { b.w.WriteHeader(c) }

func main() {
	rt := routes{}
	listen := flag.String("listen", "127.0.0.1:8180", "Adresse")
	flag.Var(rt, "route", "host=ziel (mehrfach)")
	buffer := flag.Bool("buffer", false, "Fehler nachstellen: Antworten puffern (SSE kommt verspätet)")
	strip := flag.Bool("strip-upgrade", false, "Fehler nachstellen: WebSocket-Upgrade nicht weiterreichen")
	flag.Parse()
	proxies := map[string]*httputil.ReverseProxy{}
	for host, target := range rt {
		t := target
		p := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(t)
				pr.Out.Host = pr.In.Host // Host wie nginx „proxy_set_header Host $host“
				pr.SetXForwarded()       // X-Forwarded-For/-Host/-Proto
				if *strip {
					pr.Out.Header.Del("Upgrade")
					pr.Out.Header.Del("Connection")
				}
			},
			FlushInterval: -1,
		}
		proxies[host] = p
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		p := proxies[strings.ToLower(host)]
		if p == nil {
			http.Error(w, "unbekannter Host", http.StatusMisdirectedRequest)
			return
		}
		if *buffer && r.Header.Get("Upgrade") == "" {
			p.ServeHTTP(bufferWriter{w}, r)
			return
		}
		p.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("devgateway auf %s: %d Route(n), buffer=%v, strip-upgrade=%v", *listen, len(rt), *buffer, *strip)
	log.Fatal(srv.ListenAndServe())
}
