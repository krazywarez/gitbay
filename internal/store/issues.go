package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Issue struct {
	ID        int64
	RepoID    int64
	Number    int64
	Author    string
	Title     string
	Body      string
	State     string // open | closed
	Milestone string
	CreatedAt string
	UpdatedAt string
	Labels    []string
	Assignees []string
}

type IssueComment struct {
	Author    string
	Body      string
	CreatedAt string
}

// CreateIssue allocates the per-repo number from the repo counter inside the
// same transaction as the insert — MAX(number)+1 races.
func (s *Store) CreateIssue(repoID, authorID int64, title, body string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE repos SET issue_counter = issue_counter + 1 WHERE id = ?", repoID); err != nil {
		return 0, err
	}
	var n int64
	if err := tx.QueryRow("SELECT issue_counter FROM repos WHERE id = ?", repoID).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		"INSERT INTO issues (repo_id, number, author_id, title, body) VALUES (?, ?, ?, ?, ?)",
		repoID, n, authorID, title, body); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

func (s *Store) IssueByNumber(repoID, number int64) (Issue, error) {
	var i Issue
	err := s.DB.QueryRow(`
		SELECT i.id, i.repo_id, i.number, u.username, i.title, i.body, i.state,
		       COALESCE(m.title, ''), i.created_at, i.updated_at
		FROM issues i JOIN users u ON u.id = i.author_id
		LEFT JOIN milestones m ON m.id = i.milestone_id
		WHERE i.repo_id = ? AND i.number = ?`, repoID, number).
		Scan(&i.ID, &i.RepoID, &i.Number, &i.Author, &i.Title, &i.Body, &i.State, &i.Milestone, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	if err != nil {
		return i, err
	}
	if i.Labels, err = s.issueStrings(i.ID, `
		SELECT l.name FROM issue_labels il JOIN labels l ON l.id = il.label_id
		WHERE il.issue_id = ? ORDER BY l.name`); err != nil {
		return i, err
	}
	i.Assignees, err = s.issueStrings(i.ID, `
		SELECT u.username FROM issue_assignees ia JOIN users u ON u.id = ia.user_id
		WHERE ia.issue_id = ? ORDER BY u.username`)
	return i, err
}

func (s *Store) issueStrings(issueID int64, query string) ([]string, error) {
	rows, err := s.DB.Query(query, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListIssues returns issues for a repo; state is "open", "closed", or "all".
func (s *Store) ListIssues(repoID int64, state string) ([]Issue, error) {
	q := `SELECT i.id, i.repo_id, i.number, u.username, i.title, i.body, i.state,
	             COALESCE(m.title, ''), i.created_at, i.updated_at
	      FROM issues i JOIN users u ON u.id = i.author_id
	      LEFT JOIN milestones m ON m.id = i.milestone_id
	      WHERE i.repo_id = ?`
	args := []any{repoID}
	if state != "all" {
		q += " AND i.state = ?"
		args = append(args, state)
	}
	q += " ORDER BY i.number DESC"
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.RepoID, &i.Number, &i.Author, &i.Title, &i.Body, &i.State, &i.Milestone, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) SetIssueState(issueID int64, state string) error {
	res, err := s.DB.Exec(
		"UPDATE issues SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		state, issueID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddIssueComment(issueID, authorID int64, body string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"INSERT INTO issue_comments (issue_id, author_id, body) VALUES (?, ?, ?)",
		issueID, authorID, body); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE issues SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?", issueID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListIssueComments(issueID int64) ([]IssueComment, error) {
	rows, err := s.DB.Query(`
		SELECT u.username, c.body, c.created_at
		FROM issue_comments c JOIN users u ON u.id = c.author_id
		WHERE c.issue_id = ? ORDER BY c.id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssueComment
	for rows.Next() {
		var c IssueComment
		if err := rows.Scan(&c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListIssueLabels returns the label names attached to each issue of a
// repo, keyed by issue id. Used by the web issue listing; ListIssues
// itself stays label-free for the CLI's lean list output.
func (s *Store) ListIssueLabels(repoID int64) (map[int64][]string, error) {
	rows, err := s.DB.Query(`
		SELECT il.issue_id, l.name FROM issue_labels il
		JOIN labels l ON l.id = il.label_id
		WHERE l.repo_id = ? ORDER BY l.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}

// LabelColors returns the repo's label colors keyed by label name. Labels
// with no stored color map to "".
func (s *Store) LabelColors(repoID int64) (map[string]string, error) {
	rows, err := s.DB.Query("SELECT name, color FROM labels WHERE repo_id = ?", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, color string
		if err := rows.Scan(&name, &color); err != nil {
			return nil, err
		}
		out[name] = color
	}
	return out, rows.Err()
}

// SetIssueLabel attaches (add) or detaches a label, creating the repo label
// on first use.
func (s *Store) SetIssueLabel(repoID, issueID int64, name string, add bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if add {
		if _, err := tx.Exec(
			"INSERT INTO labels (repo_id, name) VALUES (?, ?) ON CONFLICT (repo_id, name) DO NOTHING",
			repoID, name); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO issue_labels (issue_id, label_id)
			SELECT ?, id FROM labels WHERE repo_id = ? AND name = ?
			ON CONFLICT DO NOTHING`, issueID, repoID, name); err != nil {
			return err
		}
	} else {
		res, err := tx.Exec(`
			DELETE FROM issue_labels WHERE issue_id = ? AND label_id IN
			(SELECT id FROM labels WHERE repo_id = ? AND name = ?)`, issueID, repoID, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("label %q: %w", name, ErrNotFound)
		}
	}
	return tx.Commit()
}

// SetIssueAssignee adds or removes an assignee by user id.
func (s *Store) SetIssueAssignee(issueID, userID int64, add bool) error {
	if add {
		_, err := s.DB.Exec(
			"INSERT INTO issue_assignees (issue_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING",
			issueID, userID)
		return err
	}
	res, err := s.DB.Exec(
		"DELETE FROM issue_assignees WHERE issue_id = ? AND user_id = ?", issueID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordEvent appends to the event log and enqueues a delivery for every
// active webhook on the repo whose event filter matches.
func (s *Store) RecordEvent(repoID, actorID int64, kind, dataJSON string) error {
	if dataJSON == "" {
		dataJSON = "{}"
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"INSERT INTO events (repo_id, actor_id, kind, data_json) VALUES (?, ?, ?, ?)",
		repoID, actorID, kind, dataJSON)
	if err != nil {
		return err
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO webhook_deliveries (webhook_id, event_id)
		SELECT id, ? FROM webhooks
		WHERE repo_id = ? AND active = 1
		  AND (events = '*' OR ',' || events || ',' LIKE '%,' || ? || ',%')`,
		eventID, repoID, kind); err != nil {
		return err
	}
	return tx.Commit()
}
