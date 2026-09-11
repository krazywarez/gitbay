package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Milestone struct {
	ID          int64
	RepoID      int64
	OrgID       int64 // set instead of RepoID for an org milestone
	Title       string
	Description string
	DueDate     string
	State       string // open | closed
	CreatedAt   string
	OpenItems   int // open issues + open MRs attached
	ClosedItems int // closed issues + merged/closed MRs attached
}

// orgHoldsMilestone reports whether the repository's org has a milestone
// of that title; always false for a user-owned repository.
func orgHoldsMilestone(q interface {
	QueryRow(string, ...any) *sql.Row
}, repo Repo, title string) (bool, error) {
	if repo.OwnerKind != "org" {
		return false, nil
	}
	var n int
	err := q.QueryRow("SELECT COUNT(*) FROM milestones WHERE org_id = ? AND title = ?", repo.OwnerID, title).Scan(&n)
	return n > 0, err
}

// CreateMilestone creates the repository's milestone. A title the org
// holds is refused with ErrOrgScoped.
func (s *Store) CreateMilestone(repo Repo, title, description, due string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if held, err := orgHoldsMilestone(tx, repo, title); err != nil || held {
		if err != nil {
			return 0, err
		}
		return 0, ErrOrgScoped
	}
	res, err := tx.Exec(
		"INSERT INTO milestones (repo_id, title, description, due_date) VALUES (?, ?, ?, ?)",
		repo.ID, title, description, due)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("milestone %q already exists", title)
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// CreateOrgMilestone creates the org's milestone. Repositories under the
// org that hold the title are folded in: their issues and merge requests
// move to the org's row and their rows go. folded is how many were.
func (s *Store) CreateOrgMilestone(orgID int64, title, description, due string) (int64, int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"INSERT INTO milestones (org_id, title, description, due_date) VALUES (?, ?, ?, ?)",
		orgID, title, description, due)
	if err != nil {
		if isUniqueErr(err) {
			return 0, 0, fmt.Errorf("milestone %q already exists", title)
		}
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.Query(`SELECT m.id FROM milestones m JOIN repos r ON r.id = m.repo_id
		WHERE r.owner_kind = 'org' AND r.owner_id = ? AND m.title = ?`, orgID, title)
	if err != nil {
		return 0, 0, err
	}
	repoMilestones, err := scanIDs(rows)
	if err != nil {
		return 0, 0, err
	}
	for _, mid := range repoMilestones {
		if err := foldMilestoneRow(tx, id, mid); err != nil {
			return 0, 0, err
		}
	}
	return id, len(repoMilestones), tx.Commit()
}

// foldMilestoneRow moves a repository's milestone onto the org's row:
// every issue and merge request attached to it gets the org row, then the
// repository row goes.
func foldMilestoneRow(tx *sql.Tx, orgRow, repoRow int64) error {
	for _, table := range []string{"issues", "merge_requests"} {
		if _, err := tx.Exec("UPDATE "+table+" SET milestone_id = ? WHERE milestone_id = ?", orgRow, repoRow); err != nil {
			return err
		}
	}
	_, err := tx.Exec("DELETE FROM milestones WHERE id = ?", repoRow)
	return err
}

// milestoneQuery selects milestones with their progress, counting only
// items in the readable repositories. Its args come first in any query
// built on it.
func milestoneQuery(readable []int64) (string, []any) {
	in, args := inClause(readable)
	q := `
	SELECT m.id, COALESCE(m.repo_id, 0), COALESCE(m.org_id, 0), m.title, m.description, m.due_date, m.state, m.created_at,
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'open' AND i.repo_id IN ` + in + `)
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('open','source_gone') AND r.repo_id IN ` + in + `),
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'closed' AND i.repo_id IN ` + in + `)
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('merged','closed') AND r.repo_id IN ` + in + `)
	FROM milestones m`
	all := make([]any, 0, 4*len(args))
	for i := 0; i < 4; i++ {
		all = append(all, args...)
	}
	return q, all
}

func scanMilestone(row interface{ Scan(...any) error }) (Milestone, error) {
	var m Milestone
	err := row.Scan(&m.ID, &m.RepoID, &m.OrgID, &m.Title, &m.Description, &m.DueDate, &m.State,
		&m.CreatedAt, &m.OpenItems, &m.ClosedItems)
	return m, err
}

// milestoneByTitle resolves a title under where. The org's row comes
// first when both scopes are in play; creation keeps that from happening.
func (s *Store) milestoneByTitle(where string, args []any) (Milestone, error) {
	q, qargs := milestoneQuery(nil)
	m, err := scanMilestone(s.DB.QueryRow(q+" WHERE "+where+" ORDER BY m.org_id IS NULL LIMIT 1", append(qargs, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// MilestoneByTitle resolves a title the way attaching does: the org's
// milestone when the org has it, else the repository's. Progress counts
// are not populated here; list for those.
func (s *Store) MilestoneByTitle(repo Repo, title string) (Milestone, error) {
	where, args := scopeClause("m", repo)
	return s.milestoneByTitle(where+" AND m.title = ?", append(args, title))
}

func (s *Store) OrgMilestoneByTitle(orgID int64, title string) (Milestone, error) {
	return s.milestoneByTitle("m.org_id = ? AND m.title = ?", []any{orgID, title})
}

func (s *Store) listMilestones(where string, args []any, state string, readable []int64) ([]Milestone, error) {
	q, qargs := milestoneQuery(readable)
	q += " WHERE " + where
	qargs = append(qargs, args...)
	if state != "all" {
		q += " AND m.state = ?"
		qargs = append(qargs, state)
	}
	q += " ORDER BY m.org_id IS NULL, m.due_date = '', m.due_date, m.title"
	rows, err := s.DB.Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Milestone
	for rows.Next() {
		m, err := scanMilestone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMilestones lists the milestones a repository sees, the org's first,
// with progress counted over the readable repositories.
func (s *Store) ListMilestones(repo Repo, state string, readable []int64) ([]Milestone, error) {
	where, args := scopeClause("m", repo)
	return s.listMilestones(where, args, state, readable)
}

// ListOrgMilestones lists an org's milestones with progress across the
// readable repositories under it.
func (s *Store) ListOrgMilestones(orgID int64, state string, readable []int64) ([]Milestone, error) {
	return s.listMilestones("m.org_id = ?", []any{orgID}, state, readable)
}

func (s *Store) SetMilestoneState(id int64, state string) error {
	res, err := s.DB.Exec("UPDATE milestones SET state = ? WHERE id = ?", state, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetIssueMilestone attaches (or with milestoneID 0 clears) a milestone.
func (s *Store) SetIssueMilestone(issueID, milestoneID int64) error {
	return s.setItemMilestone("issues", issueID, milestoneID)
}

func (s *Store) SetMRMilestone(mrID, milestoneID int64) error {
	return s.setItemMilestone("merge_requests", mrID, milestoneID)
}

func (s *Store) setItemMilestone(table string, id, milestoneID int64) error {
	var v any
	if milestoneID != 0 {
		v = milestoneID
	}
	res, err := s.DB.Exec("UPDATE "+table+" SET milestone_id = ? WHERE id = ?", v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
