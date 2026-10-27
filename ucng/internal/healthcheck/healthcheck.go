// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package healthcheck: „<programm> healthcheck“ für Container (HEALTHCHECK, Compose) – ohne
// wget/curl im Image. Fragt /healthz auf der eigenen Adresse ab; Exit-Code 0 = gesund.
package healthcheck

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Run prüft http://127.0.0.1:<Port aus addr>/healthz und beendet das Programm.
func Run(addr string) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8080"
	}
	c := &http.Client{Timeout: 4 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ungesund:", err)
		os.Exit(1)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "ungesund: HTTP", res.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
