package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type Org struct {
	ID   int64
	Name string
}

type OrgMember struct {
	Username string
	Role     string // member | admin
}

// ownerNameTaken reports whether name is claimed by any user or org. Users
// and orgs share one namespace: /<owner>/<repo> must be unambiguous.
func ownerNameTaken(q interface {
	QueryRow(string, ...any) *sql.Row
}, name string) (bool, error) {
	var n int
	err := q.QueryRow(
		"SELECT (SELECT COUNT(*) FROM users WHERE username = ?) + (SELECT COUNT(*) FROM orgs WHERE name = ?)",
		name, name).Scan(&n)
	return n > 0, err
}

// CreateOrg makes an organization with the creator as its first admin.
func (s *Store) CreateOrg(name string, creatorID int64) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	taken, err := ownerNameTaken(tx, name)
	if err != nil {
		return 0, err
	}
	if taken {
		return 0, fmt.Errorf("the name %q is taken", name)
	}
	res, err := tx.Exec("INSERT INTO orgs (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		"INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'admin')", id, creatorID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) OrgByName(name string) (Org, error) {
	var o Org
	err := s.DB.QueryRow("SELECT id, name FROM orgs WHERE name = ?", name).Scan(&o.ID, &o.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// OrgRole returns the user's role in the org ("" for non-members).
func (s *Store) OrgRole(orgID, userID int64) (string, error) {
	var role string
	err := s.DB.QueryRow(
		"SELECT role FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Store) OrgMembers(orgID int64) ([]OrgMember, error) {
	rows, err := s.DB.Query(`
		SELECT u.username, m.role FROM org_members m
		JOIN users u ON u.id = m.user_id WHERE m.org_id = ? ORDER BY u.username`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.Username, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListOrgsForUser returns the orgs the user belongs to, with their role.
func (s *Store) ListOrgsForUser(userID int64) ([]OrgMember, error) {
	rows, err := s.DB.Query(`
		SELECT o.name, m.role FROM org_members m
		JOIN orgs o ON o.id = m.org_id WHERE m.user_id = ? ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		if err := rows.Scan(&m.Username, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetOrgMember adds a member or updates their role. Demoting the last admin
// is refused: an org must always have one.
func (s *Store) SetOrgMember(orgID, userID int64, role string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if role == "member" {
		ok, err := wouldKeepAdmin(tx, orgID, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("an organization needs at least one admin")
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = excluded.role`,
		orgID, userID, role); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveOrgMember drops a member, refusing to remove the last admin.
func (s *Store) RemoveOrgMember(orgID, userID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ok, err := wouldKeepAdmin(tx, orgID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("an organization needs at least one admin")
	}
	res, err := tx.Exec("DELETE FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// wouldKeepAdmin reports whether the org keeps at least one admin after
// userID stops being one.
func wouldKeepAdmin(tx *sql.Tx, orgID, userID int64) (bool, error) {
	var n int
	err := tx.QueryRow(
		"SELECT COUNT(*) FROM org_members WHERE org_id = ? AND role = 'admin' AND user_id <> ?",
		orgID, userID).Scan(&n)
	return n > 0, err
}

// DeleteOrg removes an empty organization; orgs still owning repositories
// are refused.
func (s *Store) DeleteOrg(orgID int64) error {
	var n int
	if err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM repos WHERE owner_kind = 'org' AND owner_id = ?", orgID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("the organization still owns %d repositories; delete or transfer them first", n)
	}
	_, err := s.DB.Exec("DELETE FROM orgs WHERE id = ?", orgID)
	return err
}

// RenameOrg changes an org's name, holding the shared owner-namespace
// invariant. The caller moves the on-disk repos directory afterward.
func (s *Store) RenameOrg(orgID int64, newName string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	taken, err := ownerNameTaken(tx, newName)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("the name %q is taken", newName)
	}
	res, err := tx.Exec("UPDATE orgs SET name = ? WHERE id = ?", newName, orgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Profile is the presentational half of a user or org.
type Profile struct {
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	About       string `json:"about,omitempty"`
	// AboutFormat is "md" or "org"; About has no filename to dispatch on.
	AboutFormat string        `json:"about_format,omitempty"`
	Links       []ProfileLink `json:"links,omitempty"`
}

// ProfileLink is one free-form link. The label is optional; a link
// without one renders as its URL.
type ProfileLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// OwnerProfile reads the profile for kind "user" or "org".
func (s *Store) OwnerProfile(kind string, id int64) (Profile, error) {
	table := map[string]string{"user": "users", "org": "orgs"}[kind]
	var p Profile
	var linksJSON string
	err := s.DB.QueryRow(
		"SELECT description, website, about, about_format, links FROM "+table+" WHERE id = ?", id).
		Scan(&p.Description, &p.Website, &p.About, &p.AboutFormat, &linksJSON)
	if err != nil {
		return p, err
	}
	if linksJSON != "" {
		if err := json.Unmarshal([]byte(linksJSON), &p.Links); err != nil {
			return p, fmt.Errorf("%s %d links: %w", kind, id, err)
		}
	}
	return p, nil
}

// SetOwnerProfile updates the profile for kind "user" or "org".
func (s *Store) SetOwnerProfile(kind string, id int64, p Profile) error {
	table := map[string]string{"user": "users", "org": "orgs"}[kind]
	if p.AboutFormat != "org" {
		p.AboutFormat = "md"
	}
	links := ""
	if len(p.Links) > 0 {
		raw, err := json.Marshal(p.Links)
		if err != nil {
			return err
		}
		links = string(raw)
	}
	_, err := s.DB.Exec(
		"UPDATE "+table+" SET description = ?, website = ?, about = ?, about_format = ?, links = ? WHERE id = ?",
		p.Description, p.Website, p.About, p.AboutFormat, links, id)
	return err
}
