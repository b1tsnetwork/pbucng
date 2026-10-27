package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Principals (PH-02, ADR-006): stabile Identität einer anmeldbaren Person. Die ID ist zufällig
// und ändert sich nie – auch nicht bei Umbenennung im AD. Verknüpft wird über (provider, subject):
//   - ldap: objectGUID (bzw. entryUUID); ohne GUID-Attribut ersatzweise „name:<benutzername>“
//   - local: Benutzername des lokalen Kontos
// DEV-Admin und Gäste bekommen keinen Principal (kein Zugang zur Kommunikation).
// Private Kontakte, Kontaktwege und Einrichtungen sind keine Principals.

type Principal struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Subject     string    `json:"subject"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	Groups      []string  `json:"groups,omitempty"`  // Mitgliedschaft in den für die Kommunikation konfigurierten Gruppen (Momentaufnahme)
	IsAdmin     bool      `json:"isAdmin,omitempty"` // Telefonbuch-Admin (Momentaufnahme)
	Disabled    bool      `json:"disabled,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	CheckedAt   time.Time `json:"checkedAt"` // letzte Bestätigung durch Anmeldung oder AD-Nachprüfung
}

func newPrincipalID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "p_" + hex.EncodeToString(b)
}

func (p Principal) clone() Principal {
	p.Groups = append([]string(nil), p.Groups...)
	return p
}

func normGroups(g []string) []string {
	out := make([]string, 0, len(g))
	seen := map[string]bool{}
	for _, x := range g {
		k := strings.ToLower(strings.TrimSpace(x))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ---------- MySQL ----------

func (m *mysqlStore) UpsertPrincipal(ctx context.Context, p *Principal) error {
	p.Groups = normGroups(p.Groups)
	groups, _ := json.Marshal(p.Groups)
	var id string
	err := m.db.QueryRowContext(ctx, `SELECT id FROM principals WHERE provider = ? AND subject = ?`, p.Provider, p.Subject).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		p.ID = newPrincipalID()
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		_, err = m.db.ExecContext(ctx, `INSERT INTO principals (id, provider, subject, username, display_name, groups_json, is_admin, disabled, created_at, checked_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Provider, p.Subject, p.Username, truncate(p.DisplayName, 200), string(groups), p.IsAdmin, p.Disabled, p.CreatedAt, p.CheckedAt)
		if isDuplicate(err) { // gleichzeitig angelegt
			return m.UpsertPrincipal(ctx, p)
		}
		return err
	case err != nil:
		return err
	}
	p.ID = id
	_, err = m.db.ExecContext(ctx, `UPDATE principals SET username = ?, display_name = ?, groups_json = ?, is_admin = ?, disabled = ?, checked_at = ? WHERE id = ?`,
		p.Username, truncate(p.DisplayName, 200), string(groups), p.IsAdmin, p.Disabled, p.CheckedAt, id)
	return err
}

func (m *mysqlStore) GetPrincipal(ctx context.Context, id string) (*Principal, error) {
	p := &Principal{ID: id}
	var groups sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT provider, subject, username, display_name, groups_json, is_admin, disabled, created_at, checked_at FROM principals WHERE id = ?`, id).
		Scan(&p.Provider, &p.Subject, &p.Username, &p.DisplayName, &groups, &p.IsAdmin, &p.Disabled, &p.CreatedAt, &p.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if groups.Valid && groups.String != "" {
		_ = json.Unmarshal([]byte(groups.String), &p.Groups)
	}
	return p, nil
}

func (m *mysqlStore) ListPrincipals(ctx context.Context) ([]Principal, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, provider, subject, username, display_name, groups_json, is_admin, disabled, created_at, checked_at FROM principals ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Principal
	for rows.Next() {
		var p Principal
		var groups sql.NullString
		if err := rows.Scan(&p.ID, &p.Provider, &p.Subject, &p.Username, &p.DisplayName, &groups, &p.IsAdmin, &p.Disabled, &p.CreatedAt, &p.CheckedAt); err != nil {
			return nil, err
		}
		if groups.Valid && groups.String != "" {
			_ = json.Unmarshal([]byte(groups.String), &p.Groups)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- JSON ----------

func (s *jsonStore) UpsertPrincipal(ctx context.Context, p *Principal) error {
	p.Groups = normGroups(p.Groups)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.principals {
		if x.Provider == p.Provider && x.Subject == p.Subject {
			p.ID, p.CreatedAt = x.ID, x.CreatedAt
			*x = p.clone()
			return s.commit()
		}
	}
	p.ID = newPrincipalID()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	c := p.clone()
	s.principals[p.ID] = &c
	return s.commit()
}

func (s *jsonStore) GetPrincipal(ctx context.Context, id string) (*Principal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x := s.principals[id]; x != nil {
		c := x.clone()
		return &c, nil
	}
	return nil, ErrNotFound
}

func (s *jsonStore) ListPrincipals(ctx context.Context) ([]Principal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Principal, 0, len(s.principals))
	for _, x := range s.principals {
		out = append(out, x.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Rückfall-Betrieb: Principals sind lesbar, aber nicht änderbar (keine neuen Anmeldungen an der
// Kommunikation, solange die Datenbank fehlt).
func (r *readOnlyStore) UpsertPrincipal(context.Context, *Principal) error { return ErrReadOnly }

// ---------- Avatar für die Kommunikation (PH-06) ----------

// CommAvatar: Startbild einer Person in der Kommunikation (PNG 128×128, vom Server neu kodiert).
// Source = Prüfsumme des Avatars im Telefonbuch, aus dem das Bild entstand; Hash = Prüfsumme des PNG.
type CommAvatar struct {
	PrincipalID string    `json:"principalId"`
	Source      string    `json:"source"`
	Hash        string    `json:"hash"`
	PNG         []byte    `json:"png"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (m *mysqlStore) SetCommAvatar(ctx context.Context, a *CommAvatar) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO comm_avatars (principal_id, source, hash, png, updated_at) VALUES (?,?,?,?,?)
		ON DUPLICATE KEY UPDATE source = VALUES(source), hash = VALUES(hash), png = VALUES(png), updated_at = VALUES(updated_at)`,
		a.PrincipalID, a.Source, a.Hash, a.PNG, a.UpdatedAt)
	return err
}

func (m *mysqlStore) GetCommAvatar(ctx context.Context, id string) (*CommAvatar, error) {
	a := &CommAvatar{PrincipalID: id}
	err := m.db.QueryRowContext(ctx, `SELECT source, hash, png, updated_at FROM comm_avatars WHERE principal_id = ?`, id).Scan(&a.Source, &a.Hash, &a.PNG, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *jsonStore) SetCommAvatar(ctx context.Context, a *CommAvatar) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commAvatars == nil {
		s.commAvatars = map[string]*CommAvatar{}
	}
	c := *a
	c.PNG = append([]byte(nil), a.PNG...)
	s.commAvatars[a.PrincipalID] = &c
	return s.commit()
}

func (s *jsonStore) GetCommAvatar(ctx context.Context, id string) (*CommAvatar, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.commAvatars[id]; c != nil {
		out := *c
		out.PNG = append([]byte(nil), c.PNG...)
		return &out, nil
	}
	return nil, ErrNotFound
}

func (r *readOnlyStore) SetCommAvatar(context.Context, *CommAvatar) error { return ErrReadOnly }
