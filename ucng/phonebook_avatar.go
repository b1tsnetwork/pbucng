// SPDX-FileCopyrightText: 2026 The UCNG authors (see NOTICE)
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Modus „phonebook“, PH-06 (ADR-015, Entscheidung des Projektinhabers 27.10.2026): Der Pixel-Avatar
// aus dem Telefonbuch ist das Startbild; die Person kann es in der Kommunikation ersetzen.
//
//   - In den Identitätsdaten steht die Prüfsumme des Startbilds (avatar). Passt sie nicht zum
//     übernommenen Bild und hat die Person kein eigenes hochgeladen, holt UCNG das PNG beim
//     Telefonbuch ab (GET /api/internal/communication/avatar/{principal}) und verarbeitet es wie
//     ein hochgeladenes Profilbild (neu kodiert, verkleinert).
//   - Eigenes Bild hochgeladen → Telefonbuch-Bild wird nicht mehr übernommen.
//   - Eigenes Bild entfernt → wieder das Startbild aus dem Telefonbuch.

import (
	"context"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

type pbAvatarJob struct{ userID, subject, hash string }

// ExtAvatarJobs: Konten, deren Startbild aus dem Telefonbuch (neu) übernommen werden soll.
func (u *Users) ExtAvatarJobs() []pbAvatarJob {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []pbAvatarJob
	for _, x := range u.byExt {
		e := x.Member.Ext
		if e.AvatarWant != "" && !e.AvatarOwn && e.AvatarWant != e.AvatarPB && e.Gone.IsZero() && !x.Member.Disabled {
			out = append(out, pbAvatarJob{x.ID, e.Subject, e.AvatarWant})
		}
	}
	return out
}

// ExtSetAvatar übernimmt das Startbild, sofern die Person inzwischen kein eigenes hochgeladen hat.
// Liefert die bisherige Datei (zum Löschen) und ob übernommen wurde.
func (u *Users) ExtSetAvatar(id, file, hash string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x := u.byID[id]
	if x == nil || x.Member == nil || x.Member.Ext == nil || x.Member.Ext.AvatarOwn {
		return "", false
	}
	old := x.Avatar
	x.Avatar, x.Member.Ext.AvatarPB = file, hash
	u.markDirty()
	return old, true
}

// ExtAvatarOwn: eigenes Bild hochgeladen (true) bzw. entfernt (false → wieder Startbild).
func (u *Users) ExtAvatarOwn(id string, own bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if x := u.byID[id]; x != nil && x.Member != nil && x.Member.Ext != nil {
		x.Member.Ext.AvatarOwn = own
		if !own {
			x.Member.Ext.AvatarPB = ""
		}
		u.markDirty()
	}
}

func (p *pbClient) avatar(ctx context.Context, subject string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.InternalURL+"/api/internal/communication/avatar/"+subject, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.key(pbKeyUCNGToPhonebook))
	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil, pbHTTPError{res.StatusCode}
	}
	return io.ReadAll(io.LimitReader(res.Body, maxUpload))
}

var pbAvatarMu sync.Mutex // eine Übernahme-Runde zur Zeit

// pbSyncAvatars übernimmt fällige Startbilder (nach Anmeldung und Nachprüfung).
func (s *Server) pbSyncAvatars(ctx context.Context) {
	if !s.hub.Media().Uploads().Avatars || s.hub.Media().Dir() == "" {
		return
	}
	pbAvatarMu.Lock()
	defer pbAvatarMu.Unlock()
	changed := false
	for _, j := range s.users.ExtAvatarJobs() {
		data, err := s.pb.avatar(ctx, j.subject)
		if err != nil {
			log.Printf("Telefonbuch: Startbild für %s nicht abrufbar: %v", j.userID, err)
			continue
		}
		ml := s.hub.Media()
		img, err := ml.runImage(data, func() (ScaledImage, error) { return ProcessAvatar(data, avatarSize) })
		if err != nil {
			log.Printf("Telefonbuch: Startbild für %s ungültig: %v", j.userID, err)
			continue
		}
		file := j.userID + "-" + randomHex(4) + "." + img.Ext
		if err := writeFileAtomic(filepath.Join(ml.Dir(), "avatars", file), img.Data); err != nil {
			log.Printf("Telefonbuch: Startbild speichern: %v", err)
			continue
		}
		old, ok := s.users.ExtSetAvatar(j.userID, file, j.hash)
		if !ok { // inzwischen eigenes Bild
			s.removeAvatarFile(file)
			continue
		}
		s.removeAvatarFile(old)
		changed = true
	}
	if changed {
		s.hub.Notify("profiles")
	}
}

func (s *Server) pbSyncAvatarsBG() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.pbSyncAvatars(ctx)
}
