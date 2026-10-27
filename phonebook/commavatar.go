package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"net/http"
	"time"
)

// Kommunikations-Add-on, PH-06 (ADR-015, Entscheidung des Projektinhabers 27.10.2026):
// Der Pixel-Avatar aus dem Telefonbuch ist das Startbild in der Kommunikation; dort lässt es sich
// durch ein eigenes Bild ersetzen.
//
//   - Gezeichnet wird der Avatar nur im Browser (web/js/avatar.js). Der Browser der Person rendert
//     deshalb ihr eigenes Bild (128×128 PNG) und lädt es hoch, sobald es nicht mehr zum Avatar passt.
//     Der Server prüft Format und Größe und kodiert das Bild neu (keine Metadaten, kein Fremdformat).
//   - In den Identitätsdaten steht die Prüfsumme des Bildes (avatar). UCNG holt es bei Bedarf
//     serverseitig ab (GET /api/internal/communication/avatar/{principal}, Schlüssel „ucng->phonebook“).

const (
	commAvatarSize    = 128
	commAvatarMaxBody = 256 << 10
)

var errCommAvatarImage = errors.New("Bild ungültig: erwartet wird ein PNG mit 128×128 Pixeln")

// avatarSource: Prüfsumme des Avatars im Telefonbuch (leer gespeichert = Standard-Avatar).
func avatarSource(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		raw = []byte("default")
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:8])
}

// cleanAvatarPNG prüft das hochgeladene Bild und kodiert es neu.
func cleanAvatarPNG(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || cfg.Width != commAvatarSize || cfg.Height != commAvatarSize {
		return nil, errCommAvatarImage
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errCommAvatarImage
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ownEmployee: eigener Telefonbucheintrag der Sitzung (nil = keiner).
func (a *App) ownEmployee(ctx context.Context, s *Session) *EmployeeRec {
	if s == nil || s.EmployeeID == 0 {
		return nil
	}
	e, err := a.st().GetEmployee(ctx, s.EmployeeID)
	if err != nil {
		return nil
	}
	return e
}

// commAvatarMe: für /api/me – passt das gespeicherte Startbild noch zum Avatar?
func (a *App) commAvatarMe(ctx context.Context, s *Session) map[string]any {
	e := a.ownEmployee(ctx, s)
	if e == nil || s.PrincipalID == "" {
		return nil
	}
	out := map[string]any{"source": avatarSource(e.Avatar), "rendered": ""}
	if c, err := a.st().GetCommAvatar(ctx, s.PrincipalID); err == nil {
		out["rendered"] = c.Source
	}
	return out
}

// PUT /api/addons/communication/avatar {"png":"<Base64>","source":"<Prüfsumme des Avatars>"}
func (a *App) handleCommAvatarPut(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	ctx := r.Context()
	if ok, reason := a.commAllowedMe(ctx, s); !ok || s.PrincipalID == "" {
		if reason == "" {
			reason = "Kommunikation nicht verfügbar"
		}
		jsonError(w, http.StatusForbidden, reason)
		return
	}
	var in struct {
		PNG    string `json:"png"`
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &in, commAvatarMaxBody) {
		return
	}
	e := a.ownEmployee(ctx, s)
	if e == nil {
		jsonError(w, http.StatusConflict, "Für dein Konto gibt es keinen Telefonbucheintrag")
		return
	}
	if in.Source != avatarSource(e.Avatar) {
		jsonError(w, http.StatusConflict, "Der Avatar wurde inzwischen geändert – bitte neu laden")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.PNG)
	if err != nil {
		jsonError(w, http.StatusBadRequest, errCommAvatarImage.Error())
		return
	}
	img, err := cleanAvatarPNG(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	h := sha256.Sum256(img)
	c := &CommAvatar{PrincipalID: s.PrincipalID, Source: in.Source, Hash: hex.EncodeToString(h[:8]), PNG: img, UpdatedAt: time.Now().UTC()}
	if err := a.st().SetCommAvatar(ctx, c); err != nil {
		dbFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hash": c.Hash, "source": c.Source})
}

// GET /api/internal/communication/avatar/{principal} (nur UCNG)
func (a *App) handleCommAvatarGet(w http.ResponseWriter, r *http.Request) {
	if !a.commPeer(w, r) {
		return
	}
	c, err := a.st().GetCommAvatar(r.Context(), r.PathValue("principal"))
	if errors.Is(err, ErrNotFound) {
		jsonError(w, http.StatusNotFound, "Kein Bild")
		return
	}
	if err != nil {
		dbFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Avatar-Hash", c.Hash)
	w.Write(c.PNG)
}

// commAvatarHash: Prüfsumme des Startbilds für die Identitätsdaten ("" = keins).
func (a *App) commAvatarHash(ctx context.Context, principal string) string {
	if c, err := a.st().GetCommAvatar(ctx, principal); err == nil {
		return c.Hash
	}
	return ""
}
