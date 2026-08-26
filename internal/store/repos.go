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
	RequireChecks        bool     `json:"require_checks,omitempty"`
	RequireApprovals     int      `json:"require_approvals,omitempty"`
	RequireResolved      bool     `json:"require_resolved,omitempty"`
	GitDaemon            bool     `json:"git_daemon,omitempty"`
	Archived             bool     `json:"archived,omitempty"`
	Website              string   `json:"website,omitempty"`
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

// SetRepoVisibility switches a repository between public and private.
func (s *Store) SetRepoVisibility(repoID int64, visibility string) error {
	if visibility != "public" && visibility != "private" {
		return fmt.Errorf("visibility must be public or private")
	}
	_, err := s.DB.Exec("UPDATE repos SET visibility = ? WHERE id = ?", visibility, repoID)
	return err
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

// ListReposForUser returns repos the user owns, reaches through an org
// (unless the org scopes members to 'none'), has an explicit grant on, or
// reaches through a team.
func (s *Store) ListReposForUser(userID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		LEFT JOIN repo_access a ON a.repo_id = r.id AND a.subject_kind = 'user' AND a.subject_id = ?
		LEFT JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.user_id = ?
		LEFT JOIN orgs og ON r.owner_kind = 'org' AND og.id = r.owner_id
		WHERE (r.owner_kind = 'user' AND r.owner_id = ?)
		   OR a.subject_id IS NOT NULL
		   OR (m.user_id IS NOT NULL AND (m.role = 'admin' OR og.members_role <> 'none'))
		   OR EXISTS (SELECT 1 FROM team_repos tr
		              JOIN team_members tm ON tm.team_id = tr.team_id AND tm.user_id = ?
		              WHERE tr.repo_id = r.id)
		GROUP BY r.id
		ORDER BY 4, r.name`, userID, userID, userID, userID)
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
// the strongest of any explicit grant, the role derived from org
// membership (org admin -> admin; plain member -> the org's members_role,
// 'write' by default so the pre-teams model is the degenerate case), and
// any team grants on the repo.
func (s *Store) AccessRole(repoID, userID int64) (string, error) {
	rank := map[string]int{"": 0, "none": 0, "read": 1, "write": 2, "admin": 3}
	best := ""
	better := func(role string) {
		if rank[role] > rank[best] {
			best = role
		}
	}

	var explicit string
	err := s.DB.QueryRow(
		"SELECT role FROM repo_access WHERE repo_id = ? AND subject_kind = 'user' AND subject_id = ?",
		repoID, userID).Scan(&explicit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	better(explicit)

	var orgRole, membersRole string
	err = s.DB.QueryRow(`
		SELECT m.role, o.members_role FROM repos r
		JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.user_id = ?
		JOIN orgs o ON o.id = r.owner_id
		WHERE r.id = ?`, userID, repoID).Scan(&orgRole, &membersRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if orgRole == "admin" {
		better("admin")
	} else if orgRole == "member" {
		better(membersRole) // write | read | none
	}

	var teamRole string
	err = s.DB.QueryRow(`
		SELECT tr.role FROM team_repos tr
		JOIN team_members tm ON tm.team_id = tr.team_id AND tm.user_id = ?
		WHERE tr.repo_id = ?
		ORDER BY CASE tr.role WHEN 'admin' THEN 3 WHEN 'write' THEN 2 ELSE 1 END DESC
		LIMIT 1`, userID, repoID).Scan(&teamRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	better(teamRole)
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

// ListReposForOwner returns every repo owned by one user or org; the caller
// filters by viewer visibility.
func (s *Store) ListReposForOwner(ownerKind string, ownerID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+" WHERE r.owner_kind = ? AND r.owner_id = ? ORDER BY r.name",
		ownerKind, ownerID)
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

// TransferRepo moves a repository to a new owner. The unique index on
// (owner_kind, owner_id, name) refuses collisions in the target namespace.
func (s *Store) TransferRepo(repoID int64, newKind string, newOwnerID int64) error {
	_, err := s.DB.Exec("UPDATE repos SET owner_kind = ?, owner_id = ? WHERE id = ?",
		newKind, newOwnerID, repoID)
	if isUniqueErr(err) {
		return fmt.Errorf("the target owner already has a repository by that name")
	}
	return err
}
