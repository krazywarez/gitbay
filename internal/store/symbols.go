package store

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

// SymbolIndex is a repository's symbol index: what was indexed and how it
// went. The states are described with the table's schema.
type SymbolIndex struct {
	ID      int64
	RepoID  int64
	Commit  string
	Tree    string
	State   string // ok | partial | failed
	Note    string
	Files   int
	Symbols int
	BuiltAt string
}

// SymbolRow is one definition. Key is the name as written where it is
// used; see internal/symbols.
type SymbolRow struct {
	ID   int64
	Name string
	Key  string
	Kind string
	Path string
	Line int
}

// SymbolTarget is where a key is defined: Count definitions, the first of
// them at Path and Line.
type SymbolTarget struct {
	Count int
	Path  string
	Line  int
}

// SymbolRequest is a repository waiting for the index worker.
type SymbolRequest struct {
	RepoID int64
	Seq    int64
	Force  bool
}

// RequestSymbolIndex queues a repository for the index worker. A request
// already waiting is bumped, so one taken by a running build is not
// cleared when that build ends; force is kept once set.
func (s *Store) RequestSymbolIndex(repoID int64, force bool) error {
	_, err := s.DB.Exec(`
		INSERT INTO symbol_requests (repo_id, force) VALUES (?, ?)
		ON CONFLICT (repo_id) DO UPDATE SET seq = seq + 1,
		       force = MAX(force, excluded.force),
		       requested_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, repoID, force)
	return err
}

// SymbolRequests lists the waiting repositories, oldest request first.
func (s *Store) SymbolRequests() ([]SymbolRequest, error) {
	rows, err := s.DB.Query(`SELECT repo_id, seq, force FROM symbol_requests ORDER BY requested_at, repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolRequest
	for rows.Next() {
		var r SymbolRequest
		if err := rows.Scan(&r.RepoID, &r.Seq, &r.Force); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DoneSymbolRequest clears a request the worker has handled, unless it was
// requested again since it was read.
func (s *Store) DoneSymbolRequest(r SymbolRequest) error {
	_, err := s.DB.Exec("DELETE FROM symbol_requests WHERE repo_id = ? AND seq = ?", r.RepoID, r.Seq)
	return err
}

// SymbolIndexFor returns a repository's index, or ErrNotFound when none
// has been built.
func (s *Store) SymbolIndexFor(repoID int64) (SymbolIndex, error) {
	var x SymbolIndex
	err := s.DB.QueryRow(`
		SELECT id, repo_id, commit_sha, tree, state, note, files, symbols, built_at
		FROM symbol_indexes WHERE repo_id = ?`, repoID).
		Scan(&x.ID, &x.RepoID, &x.Commit, &x.Tree, &x.State, &x.Note, &x.Files, &x.Symbols, &x.BuiltAt)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// symbolInsertRows is how many rows one INSERT carries: eight columns
// each, well under SQLite's variable limit.
const symbolInsertRows = 200

// ReplaceSymbolIndex swaps a repository's index for a new one in one
// transaction: readers see the old index or the new, never a mix. The
// symbols count is taken from syms.
func (s *Store) ReplaceSymbolIndex(x SymbolIndex, syms []SymbolRow) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM symbol_indexes WHERE repo_id = ?", x.RepoID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`
		INSERT INTO symbol_indexes (repo_id, commit_sha, tree, state, note, files, symbols)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, x.RepoID, x.Commit, x.Tree, x.State, x.Note, x.Files, len(syms))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for len(syms) > 0 {
		n := min(len(syms), symbolInsertRows)
		var q strings.Builder
		q.WriteString("INSERT INTO symbols (index_id, name, lname, key, lkey, kind, path, line) VALUES ")
		args := make([]any, 0, n*8)
		for i, r := range syms[:n] {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?)")
			args = append(args, id, r.Name, strings.ToLower(r.Name), r.Key, strings.ToLower(r.Key), r.Kind, r.Path, r.Line)
		}
		if _, err := tx.Exec(q.String(), args...); err != nil {
			return 0, err
		}
		syms = syms[n:]
	}
	return id, tx.Commit()
}

// SearchSymbols finds the symbols whose name or key starts with q, ignoring
// case, ranked: an exact match first, then a prefix match, each
// case-sensitive before case-insensitive, then by name, path and line.
// after is the id of the last row of the previous page, 0 for the first;
// an id that is no longer in the result gives an empty page. kind filters
// when not empty.
func (s *Store) SearchSymbols(indexID int64, q, kind string, limit int, after int64) ([]SymbolRow, error) {
	lq := strings.ToLower(q)
	hi := lq + "\U0010FFFF"
	n := utf8.RuneCountInString(q)
	lim := -1
	if limit > 0 {
		lim = limit
	}
	rows, err := s.DB.Query(`
		WITH m AS (
			SELECT id, name, key, kind, path, line,
			       CASE WHEN name = ? OR key = ? THEN 0
			            WHEN substr(name, 1, ?) = ? OR substr(key, 1, ?) = ? THEN 1
			            WHEN lname = ? OR lkey = ? THEN 2
			            ELSE 3 END AS rank
			FROM symbols
			WHERE index_id = ?
			  AND ((lname >= ? AND lname < ?) OR (lkey >= ? AND lkey < ?))
			  AND (? = '' OR kind = ?)
		)
		SELECT id, name, key, kind, path, line FROM m
		WHERE ? = 0 OR (rank, name, path, line, id) >
		      (SELECT rank, name, path, line, id FROM m WHERE id = ?)
		ORDER BY rank, name, path, line, id
		LIMIT ?`,
		q, q, n, q, n, q, lq, lq,
		indexID, lq, hi, lq, hi, kind, kind,
		after, after, lim)
	if err != nil {
		return nil, err
	}
	return scanSymbols(rows)
}

// SymbolsInFile lists one file's symbols in line order.
func (s *Store) SymbolsInFile(indexID int64, path string) ([]SymbolRow, error) {
	rows, err := s.DB.Query(`
		SELECT id, name, key, kind, path, line FROM symbols
		WHERE index_id = ? AND path = ? ORDER BY line, id`, indexID, path)
	if err != nil {
		return nil, err
	}
	return scanSymbols(rows)
}

func scanSymbols(rows *sql.Rows) ([]SymbolRow, error) {
	defer rows.Close()
	var out []SymbolRow
	for rows.Next() {
		var r SymbolRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Kind, &r.Path, &r.Line); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// symbolKeysPerQuery bounds the IN list of one SymbolTargets query.
const symbolKeysPerQuery = 500

// SymbolTargets looks up where each of keys is defined, case-sensitively.
// A key with no definition is absent from the result.
func (s *Store) SymbolTargets(indexID int64, keys []string) (map[string]SymbolTarget, error) {
	out := map[string]SymbolTarget{}
	for len(keys) > 0 {
		n := min(len(keys), symbolKeysPerQuery)
		args := make([]any, 0, n+1)
		args = append(args, indexID)
		for _, k := range keys[:n] {
			args = append(args, k)
		}
		// SQLite takes the bare columns of an aggregate query with MIN
		// from the row that holds the minimum: the first definition.
		rows, err := s.DB.Query(`
			SELECT key, COUNT(*), path, line, MIN(id) FROM symbols
			WHERE index_id = ? AND key IN (?`+strings.Repeat(",?", n-1)+`)
			GROUP BY key`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var t SymbolTarget
			var id int64
			if err := rows.Scan(&k, &t.Count, &t.Path, &t.Line, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		keys = keys[n:]
	}
	return out, nil
}
