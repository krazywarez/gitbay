package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SavedQuery is a user's named issue and merge request query (#292).
type SavedQuery struct {
	Name      string
	Query     string
	Pinned    bool
	CreatedAt string
	UpdatedAt string
}

// SaveQuery stores a query under name. replace allows overwriting one the
// user already has; without it an existing name is ErrExists. Replacing
// keeps the pin.
func (s *Store) SaveQuery(userID int64, name, query string, replace bool) error {
	if replace {
		_, err := s.DB.Exec(`
			INSERT INTO saved_queries (user_id, name, query) VALUES (?, ?, ?)
			ON CONFLICT (user_id, name) DO UPDATE SET query = excluded.query,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, userID, name, query)
		return err
	}
	_, err := s.DB.Exec("INSERT INTO saved_queries (user_id, name, query) VALUES (?, ?, ?)", userID, name, query)
	if isUniqueErr(err) {
		return ErrExists
	}
	return err
}

func (s *Store) SavedQueryByName(userID int64, name string) (SavedQuery, error) {
	var q SavedQuery
	err := s.DB.QueryRow(`SELECT name, query, pinned, created_at, updated_at
		FROM saved_queries WHERE user_id = ? AND name = ?`, userID, name).
		Scan(&q.Name, &q.Query, &q.Pinned, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

// SavedQueries returns the user's queries by name; pinnedOnly narrows to
// the ones the dashboard shows.
func (s *Store) SavedQueries(userID int64, pinnedOnly bool) ([]SavedQuery, error) {
	q := "SELECT name, query, pinned, created_at, updated_at FROM saved_queries WHERE user_id = ?"
	if pinnedOnly {
		q += " AND pinned = 1"
	}
	rows, err := s.DB.Query(q+" ORDER BY name", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedQuery
	for rows.Next() {
		var sq SavedQuery
		if err := rows.Scan(&sq.Name, &sq.Query, &sq.Pinned, &sq.CreatedAt, &sq.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sq)
	}
	return out, rows.Err()
}

func (s *Store) RemoveSavedQuery(userID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM saved_queries WHERE user_id = ? AND name = ?", userID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PinSavedQuery(userID int64, name string, pinned bool) error {
	res, err := s.DB.Exec("UPDATE saved_queries SET pinned = ? WHERE user_id = ? AND name = ?", pinned, userID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RepoScope is one repo:/owner: term. Name is a GLOB pattern; "" means
// every repository of Owner.
type RepoScope struct {
	Owner string
	Name  string
}

// ItemFilter is a parsed query, ready to run. Usernames are resolved
// (@me is the caller's name by now). Empty fields match anything.
type ItemFilter struct {
	Scopes      []RepoScope // any of them; none means every repository
	Issues, MRs bool        // which tables to read
	State       string      // open, closed, merged
	Labels      []string    // all of them
	NoLabel     bool
	Milestone   string
	NoMilestone bool
	Assignee    string // issues only
	Author      string
	Text        string // full-text over title and body
}

// ItemCursor is the sort key of the last row of a page: rows are newest
// first by creation, issues before merge requests at the same instant,
// then by id.
type ItemCursor struct {
	CreatedAt string
	Kind      int // 0 issue, 1 merge request
	ID        int64
}

// Item is one row of a cross-repository query.
type Item struct {
	Kind      string // issue or mr
	ID        int64
	RepoPath  string
	Number    int64
	Title     string
	Author    string
	State     string
	Draft     bool
	Milestone string
	CreatedAt string
	UpdatedAt string
}

// Cursor is the key a page ending on this row continues from.
func (it Item) Cursor() ItemCursor {
	k := 0
	if it.Kind == "mr" {
		k = 1
	}
	return ItemCursor{it.CreatedAt, k, it.ID}
}

// numbered collects arguments for SQL written with ?N placeholders, which
// visibleCond needs: it names the user as ?1 several times.
type numbered struct{ args []any }

func (n *numbered) add(v any) string {
	n.args = append(n.args, v)
	return fmt.Sprintf("?%d", len(n.args))
}

// itemBranch is one table's half of the query: every row of it on a
// repository the user may read (visibleCond, public or reached) that f
// admits. userID is ?1 in a.
func itemBranch(kind string, f ItemFilter, a *numbered, after *ItemCursor) string {
	table, kord, labels, draft := "issues", "0", "issue_labels il", "0"
	onItem := "il.issue_id = x.id"
	if kind == "mr" {
		table, kord, labels, draft = "merge_requests", "1", "mr_labels il", "x.draft"
		onItem = "il.mr_id = x.id"
	}
	var where []string
	where = append(where, visibleCond)
	if len(f.Scopes) > 0 {
		var scopes []string
		for _, sc := range f.Scopes {
			cond := "COALESCE(u.username, o.name) = " + a.add(sc.Owner)
			if sc.Name != "" {
				cond += " AND r.name GLOB " + a.add(sc.Name)
			}
			scopes = append(scopes, "("+cond+")")
		}
		where = append(where, "("+strings.Join(scopes, " OR ")+")")
	}
	switch {
	case f.State == "":
	case f.State == "open" && kind == "mr":
		where = append(where, "x.state IN ('open', 'source_gone')")
	default:
		where = append(where, "x.state = "+a.add(f.State))
	}
	for _, l := range f.Labels {
		where = append(where, "EXISTS (SELECT 1 FROM "+labels+" JOIN labels l ON l.id = il.label_id WHERE "+onItem+" AND l.name = "+a.add(l)+")")
	}
	if f.NoLabel {
		where = append(where, "NOT EXISTS (SELECT 1 FROM "+labels+" WHERE "+onItem+")")
	}
	if f.NoMilestone {
		where = append(where, "x.milestone_id IS NULL")
	} else if f.Milestone != "" {
		where = append(where, "ms.title = "+a.add(f.Milestone))
	}
	if f.Assignee != "" {
		where = append(where, `EXISTS (SELECT 1 FROM issue_assignees ia JOIN users iu ON iu.id = ia.user_id
			WHERE ia.issue_id = x.id AND iu.username = `+a.add(f.Assignee)+")")
	}
	if f.Author != "" {
		where = append(where, "au.username = "+a.add(f.Author))
	}
	if f.Text != "" {
		index := "issue_fts"
		if kind == "mr" {
			index = "mr_fts"
		}
		where = append(where, "x.id IN (SELECT rowid FROM "+index+" WHERE "+index+" MATCH "+a.add(FTSQuery(f.Text))+")")
	}
	if after != nil {
		where = append(where, "(x.created_at, "+kord+", x.id) < ("+a.add(after.CreatedAt)+", "+a.add(after.Kind)+", "+a.add(after.ID)+")")
	}
	return `SELECT '` + kind + `' AS kind, ` + kord + ` AS kord, x.id AS id,
	       COALESCE(u.username, o.name) || '/' || r.name, x.number, x.title, au.username,
	       x.state, ` + draft + `, COALESCE(ms.title, ''), x.created_at AS created_at, x.updated_at
	FROM ` + table + ` x
	JOIN repos r ON r.id = x.repo_id
	LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
	LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
	JOIN users au ON au.id = x.author_id
	LEFT JOIN milestones ms ON ms.id = x.milestone_id
	WHERE ` + strings.Join(where, "\n\t  AND ")
}

// itemUnion is the query over both tables f reads, or "" when it reads
// neither.
func itemUnion(userID int64, f ItemFilter, after *ItemCursor) (string, []any) {
	a := &numbered{}
	a.add(userID)
	var parts []string
	if f.Issues {
		parts = append(parts, itemBranch("issue", f, a, after))
	}
	if f.MRs {
		parts = append(parts, itemBranch("mr", f, a, after))
	}
	return strings.Join(parts, "\nUNION ALL\n"), a.args
}

// QueryItems runs f for the user across every repository they may read,
// newest first. after continues from a page's last row; limit 0 means
// every row.
func (s *Store) QueryItems(userID int64, f ItemFilter, after *ItemCursor, limit int) ([]Item, error) {
	q, args := itemUnion(userID, f, after)
	if q == "" {
		return nil, nil
	}
	q = "SELECT * FROM (" + q + ") ORDER BY created_at DESC, kord DESC, id DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var kord int
		if err := rows.Scan(&it.Kind, &kord, &it.ID, &it.RepoPath, &it.Number, &it.Title, &it.Author,
			&it.State, &it.Draft, &it.Milestone, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountItems is how many rows QueryItems would return without a limit,
// over the same readable repositories.
func (s *Store) CountItems(userID int64, f ItemFilter) (int, error) {
	q, args := itemUnion(userID, f, nil)
	if q == "" {
		return 0, nil
	}
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM ("+q+")", args...).Scan(&n)
	return n, err
}
