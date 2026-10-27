// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RC: Sicherung und Wiederherstellung im Modus phonebook. Nach dem Zurückspielen eines Archivs in
// einen leeren Datenordner gehört die Person aus dem Telefonbuch wieder zu ihrem bisherigen Konto
// (gleiche ID, Verlauf, kein zweites Konto).
func TestPhonebookBackupRestore(t *testing.T) {
	data := t.TempDir()
	h, u, s := openStore(t, data)
	res, created, err := u.ExtLogin(claimsFor("p_b_anna", "anna", "Anna Berg", ""), "test")
	if err != nil || !created {
		t.Fatalf("Konto: %v %v", created, err)
	}
	if _, err := h.Send("lobby", Poster{ID: res.ID, Name: res.Name}, "vor der Sicherung", 0); err != nil {
		t.Fatal(err)
	}
	flush(t, h, u, s)
	b := NewBackups(data, filepath.Join(t.TempDir(), "backups"), 3)
	b.store = s
	info, err := b.Create(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Path(info.Name)
	if err != nil {
		t.Fatal(err)
	}

	// Wiederherstellen: Archiv in einen leeren Datenordner entpacken (wie in deploy/stack/README.md)
	restored := t.TempDir()
	untar(t, p, restored)
	h2, u2, _ := openStore(t, restored)
	again, created, err := u2.ExtLogin(claimsFor("p_b_anna", "anna", "Anna Berg", ""), "test")
	if err != nil || created || again.ID != res.ID || again.Name != res.Name {
		t.Fatalf("nach dem Zurückspielen: neu=%v id=%s/%s name=%s err=%v", created, again.ID, res.ID, again.Name, err)
	}
	msgs, _, _ := h2.History("lobby", 0, 10)
	if len(msgs) != 1 || msgs[0].Text != "vor der Sicherung" || msgs[0].UserID != res.ID {
		t.Fatalf("Verlauf: %+v", msgs)
	}
}

func untar(t *testing.T, archive, dir string) {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Clean(hd.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			t.Fatalf("Pfad im Archiv: %s", hd.Name)
		}
		dst := filepath.Join(dir, name)
		if hd.Typeflag == tar.TypeDir {
			os.MkdirAll(dst, 0o700)
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o700)
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			t.Fatal(err)
		}
		out.Close()
	}
}
