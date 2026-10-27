package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kommunikations-Add-on, PH-05:
//
//   - „💬 Nachricht“ auf der Visitenkarte: GET /api/addons/communication/contact/{id} liefert die
//     Adresse der Kommunikation mit #pbdm:<Principal>. Dort öffnet die Oberfläche die
//     Direktnachricht (nur, wenn die Person schon einmal in der Kommunikation war).
//   - Zähler im Kopf (Entscheidung des Projektinhabers 27.10.2026: „neu seit letztem Besuch“):
//     GET /api/addons/communication/news fragt UCNG serverseitig nach der Zahl neuer
//     Direktnachrichten und Erwähnungen seit der letzten Aktivität im Chat. Nur Zahlen, nichts wird
//     gespeichert (30 s im Speicher, damit viele offene Fenster UCNG nicht belasten).

const commNewsTTL = 30 * time.Second

var (
	errCommNoAccount = errors.New("Für diesen Eintrag gibt es kein Benutzerkonto – eine Direktnachricht ist nicht möglich")
	errCommNotYet    = errors.New("Diese Person hat sich noch nicht am Telefonbuch angemeldet – eine Direktnachricht geht erst danach")
	errCommNoAccess  = errors.New("Diese Person hat keinen Zugang zur Kommunikation")
)

// commAllowedMe: darf die angemeldete Person die Kommunikation nutzen?
func (a *App) commAllowedMe(ctx context.Context, s *Session) (bool, string) {
	c := a.commMe(ctx, s)
	if c == nil {
		return false, "Kommunikation nicht verfügbar"
	}
	if ok, _ := c["allowed"].(bool); !ok {
		reason, _ := c["reason"].(string)
		if reason == "" {
			reason = "Kein Zugang zur Kommunikation"
		}
		return false, reason
	}
	return true, ""
}

// principalOfEmployee: Principal zum Konto eines Eintrags (aktiv, mit Nutzungsrecht).
func (a *App) principalOfEmployee(ctx context.Context, e *EmployeeRec) (*Principal, error) {
	if e.Username == "" {
		return nil, errCommNoAccount
	}
	list, err := a.st().ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	pol := a.commCfg()
	var best *Principal
	denied := false
	for i := range list {
		p := &list[i]
		if !strings.EqualFold(p.Username, e.Username) {
			continue
		}
		if !pol.allowed(p) { // auch gesperrt (AD) oder ausgeschieden
			denied = true
			continue
		}
		if best == nil || p.CheckedAt.After(best.CheckedAt) {
			best = p
		}
	}
	switch {
	case best != nil:
		return best, nil
	case denied:
		return nil, errCommNoAccess
	default:
		return nil, errCommNotYet
	}
}

// GET /api/addons/communication/contact/{id}
func (a *App) handleCommContact(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	ctx := r.Context()
	if ok, reason := a.commAllowedMe(ctx, s); !ok {
		jsonError(w, http.StatusForbidden, reason)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, http.StatusBadRequest, "Ungültiger Eintrag")
		return
	}
	if id == s.EmployeeID {
		jsonError(w, http.StatusBadRequest, "Das bist du selbst")
		return
	}
	e, err := a.st().GetEmployee(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && (!e.Active || e.Hidden)) {
		jsonError(w, http.StatusNotFound, "Eintrag nicht gefunden")
		return
	}
	if err != nil {
		dbFail(w, err)
		return
	}
	p, err := a.principalOfEmployee(ctx, e)
	switch {
	case errors.Is(err, errCommNoAccount), errors.Is(err, errCommNotYet), errors.Is(err, errCommNoAccess):
		jsonError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		dbFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": a.cfg.Communication.PublicBaseURL + "/#pbdm:" + p.ID})
}

// ---------- Zähler ----------

type commNewsCache struct {
	mu sync.Mutex
	m  map[string]commNewsEntry // Principal → Stand
}

type commNewsEntry struct {
	at  time.Time
	out map[string]any
}

// GET /api/addons/communication/news
func (a *App) handleCommNews(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r)
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	if ok, _ := a.commAllowedMe(ctx, s); !ok || s.PrincipalID == "" || a.comm.snapshot().Status != "ok" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	now := time.Now()
	a.commNews.mu.Lock()
	if c, ok := a.commNews.m[s.PrincipalID]; ok && now.Sub(c.at) < commNewsTTL {
		a.commNews.mu.Unlock()
		writeJSON(w, http.StatusOK, c.out)
		return
	}
	a.commNews.mu.Unlock()
	out, err := a.fetchCommNews(ctx, s.PrincipalID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	a.commNews.mu.Lock()
	if a.commNews.m == nil || len(a.commNews.m) > 5000 {
		a.commNews.m = map[string]commNewsEntry{}
	}
	a.commNews.m[s.PrincipalID] = commNewsEntry{at: now, out: out}
	a.commNews.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (a *App) fetchCommNews(ctx context.Context, principal string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"subject": principal})
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, a.cfg.Communication.InternalBaseURL+"/api/integration/phonebook/news", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Communication.key(commKeyToUCNG))
	res, err := commHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var in struct {
		DMs      int `json:"dms"`
		Mentions int `json:"mentions"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&in); err != nil {
		return nil, err
	}
	return map[string]any{"available": true, "dms": in.DMs, "mentions": in.Mentions}, nil
}
