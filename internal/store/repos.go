package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	ProtectedTags        []string `json:"protected_tags,omitempty"` // path.Match globs
	RequireSignedCommits bool     `json:"require_signed_commits,omitempty"`
	RequireChecks        bool     `json:"require_checks,omitempty"`
	RequireApprovals     int      `json:"require_approvals,omitempty"`
	RequireResolved      bool     `json:"require_resolved,omitempty"`
	RequireCodeowners    bool     `json:"require_codeowners,omitempty"`
	RequireMR            bool     `json:"require_mr,omitempty"`
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

// UpdateRepoSettings applies mutate to the repository's settings and
// stores the result, returning what was stored.
//
// settings_json is one blob, so changing one field means writing all of
// them. Callers used to read the struct off a Repo they had loaded
// earlier, change a field and write the whole blob back, which loses the
// other admin's change whenever two ran at once — last write wins over a
// value it never read. The read and the write happen here instead, inside
// one transaction, and BEGIN IMMEDIATE takes the write lock up front: a
// second updater waits at the start rather than discovering the conflict
// after it has already read a stale blob.
func (s *Store) UpdateRepoSettings(repoID int64, mutate func(*RepoSettings)) (RepoSettings, error) {
	ctx := context.Background()
	var out RepoSettings
	// The whole exchange must run on one connection for BEGIN to bracket
	// it; the pool would otherwise be free to hand the statements out
	// separately.
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return out, err
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	var raw string
	if err := conn.QueryRowContext(ctx,
		"SELECT settings_json FROM repos WHERE id = ?", repoID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrNotFound
		}
		return out, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return out, err
		}
	}
	mutate(&out)
	next, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE repos SET settings_json = ? WHERE id = ?", string(next), repoID); err != nil {
		return out, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return out, err
	}
	committed = true
	return out, nil
}

// CreateFork is CreateRepo with fork_of set in the same insert, so a fork
// never exists for a moment as a plain repository (#108).
func (s *Store) CreateFork(ownerKind string, ownerID int64, name, visibility string, forkOf int64) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO repos (owner_kind, owner_id, name, visibility, fork_of) VALUES (?, ?, ?, ?, ?)",
		ownerKind, ownerID, name, visibility, forkOf)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("repository %q already exists", name)
		}
		return 0, err
	}
	return res.LastInsertId()
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
// reaches through a team. limit 0 means everything; after (an owner/name
// path) starts the page strictly beyond it, matching the path-ascending
// order.
func (s *Store) ListReposForUser(userID int64, limit int, after string) ([]Repo, error) {
	q := repoSelect + `
		LEFT JOIN repo_access a ON a.repo_id = r.id AND a.subject_kind = 'user' AND a.subject_id = ?
		LEFT JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.user_id = ?
		LEFT JOIN orgs og ON r.owner_kind = 'org' AND og.id = r.owner_id
		WHERE ((r.owner_kind = 'user' AND r.owner_id = ?)
		   OR a.subject_id IS NOT NULL
		   OR (m.user_id IS NOT NULL AND (m.role = 'admin' OR og.members_role <> 'none'))
		   OR EXISTS (SELECT 1 FROM team_repos tr
		              JOIN team_members tm ON tm.team_id = tr.team_id AND tm.user_id = ?
		              WHERE tr.repo_id = r.id))`
	args := []any{userID, userID, userID, userID}
	if after != "" {
		owner, name, _ := strings.Cut(after, "/")
		q += ` AND (COALESCE(u.username, o.name) > ?
		         OR (COALESCE(u.username, o.name) = ? AND r.name > ?))`
		args = append(args, owner, owner, name)
	}
	q += `
		GROUP BY r.id
		ORDER BY 4, r.name`
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
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

// EffectiveEntry is one account's effective role on a repository and the
// grant it comes from: owner, direct, org admin, org member, or team
// <name>.
type EffectiveEntry struct {
	Username string
	Role     string
	Source   string
}

// EffectiveAccess lists every account that can reach a repository with
// the highest role it holds and where that role comes from. Direct
// grants, org roles and team grants are folded together the way
// AccessRole folds them for one account.
func (s *Store) EffectiveAccess(repoID int64) ([]EffectiveEntry, error) {
	rank := map[string]int{"read": 1, "write": 2, "admin": 3}
	best := map[string]EffectiveEntry{}
	var order []string
	add := func(user, role, source string) {
		if rank[role] == 0 {
			return
		}
		cur, ok := best[user]
		if !ok {
			order = append(order, user)
		}
		if !ok || rank[role] > rank[cur.Role] {
			best[user] = EffectiveEntry{user, role, source}
		}
	}
	collect := func(query string, source func(extra string) string, args ...any) error {
		rows, err := s.DB.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var user, role, extra string
			if err := rows.Scan(&user, &role, &extra); err != nil {
				return err
			}
			add(user, role, source(extra))
		}
		return rows.Err()
	}
	fixed := func(name string) func(string) string { return func(string) string { return name } }

	// The owner: a user outright, or the org's admins and members.
	if err := collect(`
		SELECT u.username, 'admin', '' FROM repos r JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		WHERE r.id = ?`, fixed("owner"), repoID); err != nil {
		return nil, err
	}
	if err := collect(`
		SELECT u.username, 'admin', '' FROM repos r
		JOIN org_members m ON r.owner_kind = 'org' AND m.org_id = r.owner_id AND m.role = 'admin'
		JOIN users u ON u.id = m.user_id WHERE r.id = ?`, fixed("org admin"), repoID); err != nil {
		return nil, err
	}
	if err := collect(`
		SELECT u.username, a.role, '' FROM repo_access a
		JOIN users u ON a.subject_kind = 'user' AND u.id = a.subject_id WHERE a.repo_id = ?`,
		fixed("direct"), repoID); err != nil {
		return nil, err
	}
	if err := collect(`
		SELECT u.username, tr.role, t.name FROM team_repos tr
		JOIN teams t ON t.id = tr.team_id
		JOIN team_members tm ON tm.team_id = tr.team_id
		JOIN users u ON u.id = tm.user_id WHERE tr.repo_id = ?
		ORDER BY CASE tr.role WHEN 'admin' THEN 3 WHEN 'write' THEN 2 ELSE 1 END DESC, t.name`,
		func(team string) string { return "team " + team }, repoID); err != nil {
		return nil, err
	}
	if err := collect(`
		SELECT u.username, o.members_role, '' FROM repos r
		JOIN orgs o ON r.owner_kind = 'org' AND o.id = r.owner_id
		JOIN org_members m ON m.org_id = o.id AND m.role = 'member'
		JOIN users u ON u.id = m.user_id WHERE r.id = ?`, fixed("org member"), repoID); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]EffectiveEntry, 0, len(order))
	for _, u := range order {
		out = append(out, best[u])
	}
	return out, nil
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

// ListForks returns the repositories forked from one repo. The caller
// filters by what the viewer may see.
func (s *Store) ListForks(repoID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+" WHERE r.fork_of = ? ORDER BY 4, r.name", repoID)
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

// RenameRepo changes a repository's name under the same owner. The unique
// index on (owner_kind, owner_id, name) refuses collisions.
func (s *Store) RenameRepo(repoID int64, newName string) error {
	_, err := s.DB.Exec("UPDATE repos SET name = ? WHERE id = ?", newName, repoID)
	if isUniqueErr(err) {
		return fmt.Errorf("the owner already has a repository by that name")
	}
	return err
}

// TransferRepo moves a repository to a new owner. The unique index on
// (owner_kind, owner_id, name) refuses collisions in the target namespace.
// Moving into an org folds the repository's labels and milestones whose
// names the org already holds into the org's rows, in the same
// transaction, so the repository does not come out seeing two of each.
// Moving out of an org needs no counterpart: the repository keeps what it
// owns and stops seeing the org's rows.
func (s *Store) TransferRepo(repoID int64, newKind string, newOwnerID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE repos SET owner_kind = ?, owner_id = ? WHERE id = ?",
		newKind, newOwnerID, repoID); err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("the target owner already has a repository by that name")
		}
		return err
	}
	if newKind == "org" {
		if err := foldIntoOrg(tx, repoID, newOwnerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// foldIntoOrg folds a repository's labels and milestones into the org's
// rows of the same name, the way org label set and org milestone create
// fold the repositories already under the org.
func foldIntoOrg(tx *sql.Tx, repoID, orgID int64) error {
	labels, err := sharedNameRows(tx, "labels", "name", repoID, orgID)
	if err != nil {
		return err
	}
	for _, p := range labels {
		if err := foldLabelRow(tx, p.org, p.repo); err != nil {
			return err
		}
	}
	milestones, err := sharedNameRows(tx, "milestones", "title", repoID, orgID)
	if err != nil {
		return err
	}
	for _, p := range milestones {
		if err := foldMilestoneRow(tx, p.org, p.repo); err != nil {
			return err
		}
	}
	return nil
}

// rowPair is one repository row and the org row it folds into.
type rowPair struct{ repo, org int64 }

// sharedNameRows pairs a repository's label or milestone rows with the
// org's rows carrying the same name.
func sharedNameRows(tx *sql.Tx, table, nameCol string, repoID, orgID int64) ([]rowPair, error) {
	rows, err := tx.Query("SELECT t.id, o.id FROM "+table+" t JOIN "+table+" o"+
		" ON o.org_id = ? AND o."+nameCol+" = t."+nameCol+" WHERE t.repo_id = ?", orgID, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rowPair
	for rows.Next() {
		var p rowPair
		if err := rows.Scan(&p.repo, &p.org); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
