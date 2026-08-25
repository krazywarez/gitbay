package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Team struct {
	ID    int64
	OrgID int64
	Name  string
}

func (s *Store) CreateTeam(orgID int64, name string) (int64, error) {
	res, err := s.DB.Exec("INSERT INTO teams (org_id, name) VALUES (?, ?)", orgID, name)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("team %q already exists", name)
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) TeamByName(orgID int64, name string) (Team, error) {
	var t Team
	err := s.DB.QueryRow("SELECT id, org_id, name FROM teams WHERE org_id = ? AND name = ?",
		orgID, name).Scan(&t.ID, &t.OrgID, &t.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) DeleteTeam(teamID int64) error {
	res, err := s.DB.Exec("DELETE FROM teams WHERE id = ?", teamID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListTeams(orgID int64) ([]Team, error) {
	rows, err := s.DB.Query("SELECT id, org_id, name FROM teams WHERE org_id = ? ORDER BY name", orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AddTeamMember(teamID, userID int64) error {
	_, err := s.DB.Exec(
		"INSERT INTO team_members (team_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING",
		teamID, userID)
	return err
}

func (s *Store) RemoveTeamMember(teamID, userID int64) error {
	res, err := s.DB.Exec("DELETE FROM team_members WHERE team_id = ? AND user_id = ?", teamID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TeamMembers(teamID int64) ([]string, error) {
	rows, err := s.DB.Query(`
		SELECT u.username FROM team_members tm JOIN users u ON u.id = tm.user_id
		WHERE tm.team_id = ? ORDER BY u.username`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GrantTeamRepo attaches (or updates) a team's role on a repo.
func (s *Store) GrantTeamRepo(teamID, repoID int64, role string) error {
	_, err := s.DB.Exec(`
		INSERT INTO team_repos (team_id, repo_id, role) VALUES (?, ?, ?)
		ON CONFLICT (team_id, repo_id) DO UPDATE SET role = excluded.role`,
		teamID, repoID, role)
	return err
}

func (s *Store) RevokeTeamRepo(teamID, repoID int64) error {
	res, err := s.DB.Exec("DELETE FROM team_repos WHERE team_id = ? AND repo_id = ?", teamID, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type TeamGrant struct {
	RepoPath string `json:"repo"`
	Role     string `json:"role"`
}

func (s *Store) TeamGrants(teamID int64) ([]TeamGrant, error) {
	rows, err := s.DB.Query(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name, tr.role
		FROM team_repos tr JOIN repos r ON r.id = tr.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs  o ON r.owner_kind = 'org'  AND o.id = r.owner_id
		WHERE tr.team_id = ? ORDER BY 1`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TeamGrant
	for rows.Next() {
		var g TeamGrant
		if err := rows.Scan(&g.RepoPath, &g.Role); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SetOrgMembersRole(orgID int64, role string) error {
	_, err := s.DB.Exec("UPDATE orgs SET members_role = ? WHERE id = ?", role, orgID)
	return err
}
