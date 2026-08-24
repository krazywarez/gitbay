package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Repo struct {
	ID            int64
	OwnerKind     string // user | org
	OwnerID       int64
	OwnerName     string // resolved for display and disk paths
	Name          string
	Visibility    string // public | private
	DefaultBranch string
	ForkOf        int64 // 0 when not a fork
	Settings      RepoSettings
}

type RepoSettings struct {
	ProtectedBranches    []string `json:"protected_branches,omitempty"`
	RequireSignedCommits bool     `json:"require_signed_commits,omitempty"`
	GitDaemon            bool     `json:"git_daemon,omitempty"`
}

// Path returns the canonical owner/name form.
func (r Repo) Path() string { return r.OwnerName + "/" + r.Name }

func (s *Store) CreateRepo(ownerKind string, ownerID int64, name, visibility string) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES (?, ?, ?, ?)",
		ownerKind, ownerID, name, visibility)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("repository %q already exists", name)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// repoSelect resolves the owner name from whichever table owns the repo.
const repoSelect = `
	SELECT r.id, r.owner_kind, r.owner_id, COALESCE(u.username, o.name),
	       r.name, r.visibility, r.default_branch, COALESCE(r.fork_of, 0), r.settings_json
	FROM repos r
	LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
	LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id`

func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var settingsJSON string
	err := row.Scan(&r.ID, &r.OwnerKind, &r.OwnerID, &r.OwnerName, &r.Name, &r.Visibility, &r.DefaultBranch, &r.ForkOf, &settingsJSON)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(settingsJSON), &r.Settings); err != nil {
		return r, fmt.Errorf("repo %d settings: %w", r.ID, err)
	}
	return r, nil
}

// RepoByPath resolves "owner/name"; the owner may be a user or an org.
func (s *Store) RepoByPath(path string) (Repo, error) {
	owner, name, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git"), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("%w: repository path must be owner/name", ErrNotFound)
	}
	r, err := scanRepo(s.DB.QueryRow(
		repoSelect+" WHERE COALESCE(u.username, o.name) = ? AND r.name = ?", owner, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return r, err
}

func (s *Store) SetRepoSettings(repoID int64, settings RepoSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("UPDATE repos SET settings_json = ? WHERE id = ?", string(raw), repoID)
	return err
}

func (s *Store) SetForkOf(repoID, parentID int64) error {
	_, err := s.DB.Exec("UPDATE repos SET fork_of = ? WHERE id = ?", parentID, repoID)
	return err
}

func (s *Store) DeleteRepo(repoID int64) error {
	res, err := s.DB.Exec("DELETE FROM repos WHERE id = ?", repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListReposForUser returns repos the user owns, belongs to through an org,
// or has an explicit grant on.
func (s *Store) ListReposForUser(userID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		LEFT JOIN repo_access a ON a.repo_id = r.id AND a.subject_kind = 'user' AND a.subject_id = ?
		LEFT JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.user_id = ?
		WHERE (r.owner_kind = 'user' AND r.owner_id = ?) OR a.subject_id IS NOT NULL OR m.user_id IS NOT NULL
		GROUP BY r.id
		ORDER BY 4, r.name`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccessRole returns the user's effective role on the repo ("" if none):
// the strongest of any explicit grant and, for org-owned repos, the role
// derived from org membership (org admin -> admin, org member -> write).
func (s *Store) AccessRole(repoID, userID int64) (string, error) {
	rank := map[string]int{"": 0, "read": 1, "write": 2, "admin": 3}
	best := ""

	var explicit string
	err := s.DB.QueryRow(
		"SELECT role FROM repo_access WHERE repo_id = ? AND subject_kind = 'user' AND subject_id = ?",
		repoID, userID).Scan(&explicit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if rank[explicit] > rank[best] {
		best = explicit
	}

	var orgRole string
	err = s.DB.QueryRow(`
		SELECT m.role FROM repos r
		JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.user_id = ?
		WHERE r.id = ?`, userID, repoID).Scan(&orgRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	derived := map[string]string{"admin": "admin", "member": "write"}[orgRole]
	if rank[derived] > rank[best] {
		best = derived
	}
	return best, nil
}

func (s *Store) GrantAccess(repoID, userID int64, role string) error {
	_, err := s.DB.Exec(`
		INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'user', ?, ?)
		ON CONFLICT (repo_id, subject_kind, subject_id) DO UPDATE SET role = excluded.role`,
		repoID, userID, role)
	return err
}

func (s *Store) RevokeAccess(repoID, userID int64) error {
	res, err := s.DB.Exec(
		"DELETE FROM repo_access WHERE repo_id = ? AND subject_kind = 'user' AND subject_id = ?",
		repoID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type AccessEntry struct {
	Username string
	Role     string
}

func (s *Store) ListAccess(repoID int64) ([]AccessEntry, error) {
	rows, err := s.DB.Query(`
		SELECT u.username, a.role FROM repo_access a
		JOIN users u ON a.subject_kind = 'user' AND u.id = a.subject_id
		WHERE a.repo_id = ? ORDER BY u.username`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessEntry
	for rows.Next() {
		var e AccessEntry
		if err := rows.Scan(&e.Username, &e.Role); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RepoByID(id int64) (Repo, error) {
	r, err := scanRepo(s.DB.QueryRow(repoSelect+" WHERE r.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	return r, err
}

// ListPublicRepos returns all public repositories, for the anonymous index.
func (s *Store) ListPublicRepos() ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect + " WHERE r.visibility = 'public' ORDER BY 4, r.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateDefaultBranch(repoID int64, branch string) error {
	_, err := s.DB.Exec("UPDATE repos SET default_branch = ? WHERE id = ?", branch, repoID)
	return err
}
