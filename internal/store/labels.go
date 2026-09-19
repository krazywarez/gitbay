package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Label is a label with its colour, "" when none was set (the web then
// derives one from the name), and how many issues and merge requests
// carry it. Org is true for a label the repository sees through its org.
type Label struct {
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
	Org    bool   `json:"org,omitempty"`
	Issues int64  `json:"issues"`
	MRs    int64  `json:"mrs"`
}

// labelJoin is where a labelled thing carries its labels. Issues and
// merge requests attach them identically, differing only in the join
// table, its column naming the thing, and the thing's own table.
type labelJoin struct {
	table string
	item  string
	items string
}

var (
	issueLabelJoin = labelJoin{"issue_labels", "issue_id", "issues"}
	mrLabelJoin    = labelJoin{"mr_labels", "mr_id", "merge_requests"}
)

// labelRows lists labels under where, with use counted over the issues
// and merge requests of the readable repositories only, so a private
// repository's does not show in a count someone outside it can see.
func (s *Store) labelRows(where string, args []any, readable []int64) ([]Label, error) {
	in, inArgs := inClause(readable)
	q := `SELECT l.name, l.color, l.org_id IS NOT NULL,
		(SELECT COUNT(*) FROM issue_labels il JOIN issues i ON i.id = il.issue_id
		 WHERE il.label_id = l.id AND i.repo_id IN ` + in + `),
		(SELECT COUNT(*) FROM mr_labels ml JOIN merge_requests m ON m.id = ml.mr_id
		 WHERE ml.label_id = l.id AND m.repo_id IN ` + in + `)
		FROM labels l WHERE ` + where + ` ORDER BY l.org_id IS NULL, l.name`
	rows, err := s.DB.Query(q, append(append(append([]any{}, inArgs...), inArgs...), args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.Name, &l.Color, &l.Org, &l.Issues, &l.MRs); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// listItemLabels returns the label names attached to each of a
// repository's issues or merge requests, keyed by its row id, the org's
// labels included.
func (s *Store) listItemLabels(j labelJoin, repo Repo) (map[int64][]string, error) {
	where, args := scopeClause("l", repo)
	rows, err := s.DB.Query(`
		SELECT j.`+j.item+`, l.name FROM `+j.table+` j
		JOIN labels l ON l.id = j.label_id
		JOIN `+j.items+` t ON t.id = j.`+j.item+`
		WHERE t.repo_id = ? AND `+where+` ORDER BY l.name`, append([]any{repo.ID}, args...)...)
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

// setItemLabel attaches (add) or detaches a label by name. Adding
// resolves the org's row when the org has the name, else the
// repository's, creating that on first use.
func (s *Store) setItemLabel(j labelJoin, repo Repo, itemID int64, name string, add bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where, args := scopeClause("l", repo)
	if add {
		if held, err := orgHoldsLabel(tx, repo, name); err != nil {
			return err
		} else if !held {
			if _, err := tx.Exec(`INSERT INTO labels (repo_id, name) VALUES (?, ?)
				ON CONFLICT (repo_id, name) WHERE repo_id IS NOT NULL DO NOTHING`, repo.ID, name); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO `+j.table+` (`+j.item+`, label_id)
			SELECT ?, l.id FROM labels l WHERE `+where+` AND l.name = ?
			ORDER BY l.org_id IS NULL LIMIT 1
			ON CONFLICT DO NOTHING`, append(append([]any{itemID}, args...), name)...); err != nil {
			return err
		}
	} else {
		res, err := tx.Exec(`DELETE FROM `+j.table+` WHERE `+j.item+` = ? AND label_id IN
			(SELECT l.id FROM labels l WHERE `+where+` AND l.name = ?)`,
			append(append([]any{itemID}, args...), name)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("label %q: %w", name, ErrNotFound)
		}
	}
	return tx.Commit()
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

// DeleteLabel removes the repository's label and takes it off every issue
// and merge request. An org's label is ErrOrgScoped; no label at all is
// ErrNotFound.
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
// under the org that hold the name are folded in: their issues and merge
// requests move to the org's row and their rows go. folded is how many
// were.
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
	repoRows, err := scanIDs(rows)
	if err != nil {
		return 0, err
	}
	for _, id := range repoRows {
		if err := foldLabelRow(tx, orgRow, id); err != nil {
			return 0, err
		}
	}
	return len(repoRows), tx.Commit()
}

// foldLabelRow moves a repository's label onto the org's row: every issue
// and merge request carrying it gets the org row, then the repository row
// goes.
func foldLabelRow(tx *sql.Tx, orgRow, repoRow int64) error {
	// OR IGNORE: nothing can carry both today, but the primary key makes
	// the move safe if it ever did.
	for _, table := range []string{issueLabelJoin.table, mrLabelJoin.table} {
		if _, err := tx.Exec("UPDATE OR IGNORE "+table+" SET label_id = ? WHERE label_id = ?", orgRow, repoRow); err != nil {
			return err
		}
	}
	_, err := tx.Exec("DELETE FROM labels WHERE id = ?", repoRow)
	return err
}

// DeleteOrgLabel removes an org's label from the org and from every issue
// and merge request under it.
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
