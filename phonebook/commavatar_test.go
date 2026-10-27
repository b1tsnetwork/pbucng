package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"
)

// PH-06 (ADR-015): Pixel-Avatar als Startbild in der Kommunikation.

func pngOf(t *testing.T, w, h int, withText bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 99, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	if !withText {
		return b.Bytes()
	}
	// tEXt-Block vor IEND einschieben (Metadaten, die der Server entfernen muss)
	raw := b.Bytes()
	body := []byte("tEXtGeheim\x00xyzqq")
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(body)-4))
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
	return append(append(append([]byte{}, raw[:len(raw)-12]...), chunk...), raw[len(raw)-12:]...)
}

func TestCommAvatar(t *testing.T) {
	e := ssoApp(t, nil)
	f := makeOrg(t, e.a)
	ctx := context.Background()
	ca, csrf := e.login(t, "anna")
	me := func() map[string]any {
		var out map[string]any
		json.Unmarshal(getWith(e.h, "/api/me", ca, "10.0.0.5").Body.Bytes(), &out)
		c, _ := out["communication"].(map[string]any)
		av, _ := c["avatar"].(map[string]any)
		return av
	}
	put := func(body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		rec := doReq(e.h, "PUT", "/api/addons/communication/avatar", string(b), "10.0.0.5", map[string]string{"Cookie": ca.String(), "X-CSRF-Token": csrf, "Content-Type": "application/json"})
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	av := me()
	if av == nil || av["source"] == "" || av["rendered"] != "" {
		t.Fatalf("Ausgangslage: %v", av)
	}
	src := av["source"].(string)
	enc := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	var jp bytes.Buffer
	jpeg.Encode(&jp, image.NewRGBA(image.Rect(0, 0, 128, 128)), nil)
	for _, c := range []struct {
		body any
		want int
		what string
	}{
		{map[string]string{"png": enc(pngOf(t, 129, 128, false)), "source": src}, 400, "falsche Größe"},
		{map[string]string{"png": enc(jp.Bytes()), "source": src}, 400, "JPEG"},
		{map[string]string{"png": "kein base64!", "source": src}, 400, "kein Base64"},
		{map[string]string{"png": enc(pngOf(t, 128, 128, false)), "source": "veraltet"}, 409, "veralteter Avatar"},
	} {
		if st, out := put(c.body); st != c.want {
			t.Fatalf("%s: %d %v", c.what, st, out)
		}
	}
	// ohne CSRF-Token abgelehnt
	b, _ := json.Marshal(map[string]string{"png": enc(pngOf(t, 128, 128, false)), "source": src})
	if rec := doReq(e.h, "PUT", "/api/addons/communication/avatar", string(b), "10.0.0.5", map[string]string{"Cookie": ca.String(), "Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Fatalf("ohne CSRF: %d", rec.Code)
	}
	st, out := put(map[string]string{"png": enc(pngOf(t, 128, 128, true)), "source": src})
	if st != 200 || out["hash"] == "" {
		t.Fatalf("hochladen: %d %v", st, out)
	}
	hash := out["hash"].(string)
	if av = me(); av["rendered"] != src {
		t.Fatalf("nach dem Hochladen: %v", av)
	}
	// Identitätsdaten tragen die Prüfsumme; UCNG holt das Bild mit seinem Schlüssel
	code, ver := e.codeFor(t, ca)
	if st, cl := e.redeem(code, ver); st != 200 || cl.Avatar != hash {
		t.Fatalf("Identitätsdaten: %d %q", st, cl.Avatar)
	}
	p, _ := e.a.principalOfEmployee(ctx, &EmployeeRec{Username: "anna"})
	rec := doReq(e.h, "GET", "/api/internal/communication/avatar/"+p.ID, "", "172.18.0.9", map[string]string{"Authorization": "Bearer " + e.a.cfg.Communication.key(commKeyFromUCNG)})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || bytes.Contains(rec.Body.Bytes(), []byte("Geheim")) {
		t.Fatalf("Abruf durch UCNG: %d %s (Metadaten entfernt: %v)", rec.Code, rec.Header().Get("Content-Type"), !bytes.Contains(rec.Body.Bytes(), []byte("Geheim")))
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes())); err != nil || cfg.Width != 128 {
		t.Fatalf("Bild: %v %v", cfg, err)
	}
	for _, key := range []string{"", e.a.cfg.Communication.key(commKeyToUCNG)} {
		if rec := doReq(e.h, "GET", "/api/internal/communication/avatar/"+p.ID, "", "172.18.0.10", map[string]string{"Authorization": "Bearer " + key}); rec.Code != 401 {
			t.Fatalf("Abruf mit falschem Schlüssel: %d", rec.Code)
		}
	}
	// Avatar im Telefonbuch geändert → Startbild gilt als veraltet
	e.a.st().SetAvatar(ctx, f.anna, json.RawMessage(`{"v":2,"skin":"#aa8866"}`))
	if av = me(); av["source"] == src || av["rendered"] != src {
		t.Fatalf("nach Avatar-Änderung: %v", av)
	}
}
