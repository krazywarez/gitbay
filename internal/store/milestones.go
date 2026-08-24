package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Milestone struct {
	ID          int64
	RepoID      int64
	Title       string
	Description string
	DueDate     string
	State       string // open | closed
	CreatedAt   string
	OpenItems   int // open issues + open MRs attached
	ClosedItems int // closed issues + merged/closed MRs attached
}

func (s *Store) CreateMilestone(repoID int64, title, description, due string) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO milestones (repo_id, title, description, due_date) VALUES (?, ?, ?, ?)",
		repoID, title, description, due)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("milestone %q already exists", title)
		}
		return 0, err
	}
	return res.LastInsertId()
}

const milestoneSelect = `
	SELECT m.id, m.repo_id, m.title, m.description, m.due_date, m.state, m.created_at,
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'open')
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('open','source_gone')),
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'closed')
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('merged','closed'))
	FROM milestones m`

func scanMilestone(row interface{ Scan(...any) error }) (Milestone, error) {
	var m Milestone
	err := row.Scan(&m.ID, &m.RepoID, &m.Title, &m.Description, &m.DueDate, &m.State,
		&m.CreatedAt, &m.OpenItems, &m.ClosedItems)
	return m, err
}

func (s *Store) MilestoneByTitle(repoID int64, title string) (Milestone, error) {
	m, err := scanMilestone(s.DB.QueryRow(
		milestoneSelect+" WHERE m.repo_id = ? AND m.title = ?", repoID, title))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func (s *Store) ListMilestones(repoID int64, state string) ([]Milestone, error) {
	q := milestoneSelect + " WHERE m.repo_id = ?"
	args := []any{repoID}
	if state != "all" {
		q += " AND m.state = ?"
		args = append(args, state)
	}
	q += " ORDER BY m.due_date = '', m.due_date, m.title"
	rows, err := s.DB.Query(q, args...)
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
