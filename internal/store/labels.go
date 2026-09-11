package store

import (
	"database/sql"
	"errors"
)

// Label is an issue label with its colour, "" when none was set (the web
// then derives one from the name), and how many issues carry it. Org is
// true for a label the repository sees through its org.
type Label struct {
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
	Org    bool   `json:"org,omitempty"`
	Issues int64  `json:"issues"`
}

// labelRows lists labels under where, with use counted over the issues of
// the readable repositories only, so a private repository's issues do not
// show in a count someone outside it can see.
func (s *Store) labelRows(where string, args []any, readable []int64) ([]Label, error) {
	in, inArgs := inClause(readable)
	q := `SELECT l.name, l.color, l.org_id IS NOT NULL,
		(SELECT COUNT(*) FROM issue_labels il JOIN issues i ON i.id = il.issue_id
		 WHERE il.label_id = l.id AND i.repo_id IN ` + in + `)
		FROM labels l WHERE ` + where + ` ORDER BY l.org_id IS NULL, l.name`
	rows, err := s.DB.Query(q, append(inArgs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.Name, &l.Color, &l.Org, &l.Issues); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListLabels lists the labels a repository sees: its org's first, then its
// own, each by name.
func (s *Store) ListLabels(repo Repo, readable []int64) ([]Label, error) {
	where, args := scopeClause("l", repo)
	return s.labelRows(where, args, readable)
}

// ListOrgLabels lists an org's labels.
func (s *Store) ListOrgLabels(orgID int64, readable []int64) ([]Label, error) {
	return s.labelRows("l.org_id = ?", []any{orgID}, readable)
}

// LabelByName resolves a name the way attaching does: the org's row when
// the org has it, else the repository's.
func (s *Store) LabelByName(repo Repo, name string) (Label, error) {
	where, args := scopeClause("l", repo)
	var l Label
	err := s.DB.QueryRow(`SELECT l.name, l.color, l.org_id IS NOT NULL FROM labels l
		WHERE `+where+` AND l.name = ? ORDER BY l.org_id IS NULL LIMIT 1`,
		append(args, name)...).Scan(&l.Name, &l.Color, &l.Org)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// orgHoldsLabel reports whether the repository's org has a label of that
// name; always false for a user-owned repository.
func orgHoldsLabel(q interface {
	QueryRow(string, ...any) *sql.Row
}, repo Repo, name string) (bool, error) {
	if repo.OwnerKind != "org" {
		return false, nil
	}
	var n int
	err := q.QueryRow("SELECT COUNT(*) FROM labels WHERE org_id = ? AND name = ?", repo.OwnerID, name).Scan(&n)
	return n > 0, err
}

// SetLabel creates the repository's label or sets its colour. A name the
// org holds is refused with ErrOrgScoped.
func (s *Store) SetLabel(repo Repo, name, color string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if held, err := orgHoldsLabel(tx, repo, name); err != nil || held {
		if err != nil {
			return err
		}
		return ErrOrgScoped
	}
	_, err = tx.Exec(`INSERT INTO labels (repo_id, name, color) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, name) WHERE repo_id IS NOT NULL DO UPDATE SET color = excluded.color`,
		repo.ID, name, color)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteLabel removes the repository's label and takes it off every issue.
// An org's label is ErrOrgScoped; no label at all is ErrNotFound.
func (s *Store) DeleteLabel(repo Repo, name string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM labels WHERE repo_id = ? AND name = ?", repo.ID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return tx.Commit()
	}
	if held, err := orgHoldsLabel(tx, repo, name); err != nil || held {
		if err != nil {
			return err
		}
		return ErrOrgScoped
	}
	return ErrNotFound
}

// SetOrgLabel creates the org's label or sets its colour. Repositories
// under the org that hold the name are folded in: their issues move to
// the org's row and their rows go. folded is how many were.
func (s *Store) SetOrgLabel(orgID int64, name, color string) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO labels (org_id, name, color) VALUES (?, ?, ?)
		ON CONFLICT (org_id, name) WHERE org_id IS NOT NULL DO UPDATE SET color = excluded.color`,
		orgID, name, color); err != nil {
		return 0, err
	}
	var orgRow int64
	if err := tx.QueryRow("SELECT id FROM labels WHERE org_id = ? AND name = ?", orgID, name).Scan(&orgRow); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT l.id FROM labels l JOIN repos r ON r.id = l.repo_id
		WHERE r.owner_kind = 'org' AND r.owner_id = ? AND l.name = ?`, orgID, name)
	if err != nil {
		return 0, err
	}
	var repoRows []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		repoRows = append(repoRows, id)
	}
	rows.Close()
	for _, id := range repoRows {
		// OR IGNORE: an issue cannot carry both today, but the primary key
		// makes the move safe if it ever did.
		if _, err := tx.Exec("UPDATE OR IGNORE issue_labels SET label_id = ? WHERE label_id = ?", orgRow, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec("DELETE FROM labels WHERE id = ?", id); err != nil {
			return 0, err
		}
	}
	return len(repoRows), tx.Commit()
}

// DeleteOrgLabel removes an org's label from the org and from every issue
// under it.
func (s *Store) DeleteOrgLabel(orgID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM labels WHERE org_id = ? AND name = ?", orgID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
