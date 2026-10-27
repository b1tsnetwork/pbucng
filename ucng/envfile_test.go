// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// RC: Geheimnisse aus Dateien (<NAME>_FILE, z. B. Docker-Secrets).
func TestEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(p, []byte("geheim-aus-datei\n"), 0o600)
	t.Setenv("UCNG_TEST_PW_FILE", p)
	if got := env("UCNG_TEST_PW", "x"); got != "geheim-aus-datei" {
		t.Fatalf("aus Datei: %q", got)
	}
	t.Setenv("UCNG_TEST_PW", "direkt")
	if got := env("UCNG_TEST_PW", "x"); got != "direkt" {
		t.Fatalf("Umgebung hat Vorrang: %q", got)
	}
	if got := env("UCNG_TEST_NICHTS", "standard"); got != "standard" {
		t.Fatalf("Standard: %q", got)
	}
}
