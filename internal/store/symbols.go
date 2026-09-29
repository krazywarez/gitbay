package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SymbolIndex is one symbol index of a repository. The states are
// described with the table's schema; reads only ever see ok or partial.
type SymbolIndex struct {
	ID      int64
	RepoID  int64
	Commit  string
	Tree    string
	State   string // building | ok | partial | retired
	Note    string
	Files   int
	Symbols int
	BuiltAt string
}

// SymbolFailure is the last run that could not build an index.
type SymbolFailure struct {
	Tree     string
	Note     string
	FailedAt string
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
	RepoID   int64
	Seq      int64
	Force    bool
	Attempts int
}

// RequestSymbolIndex queues a repository for the index worker. A request
// already waiting is bumped, so one taken by a running build is not
// cleared when that build ends, and a deferred retry becomes due now;
// force is kept once set.
func (s *Store) RequestSymbolIndex(repoID int64, force bool) error {
	_, err := s.DB.Exec(`
		INSERT INTO symbol_requests (repo_id, force) VALUES (?, ?)
		ON CONFLICT (repo_id) DO UPDATE SET seq = seq + 1,
		       force = MAX(force, excluded.force), attempts = 0, not_before = '',
		       requested_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, repoID, force)
	return err
}

// SymbolRequests lists the requests that are due, oldest first.
func (s *Store) SymbolRequests() ([]SymbolRequest, error) {
	rows, err := s.DB.Query(`
		SELECT repo_id, seq, force, attempts FROM symbol_requests
		WHERE not_before = '' OR not_before <= strftime('%Y-%m-%dT%H:%M:%fZ','now')
		ORDER BY requested_at, repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolRequest
	for rows.Next() {
		var r SymbolRequest
		if err := rows.Scan(&r.RepoID, &r.Seq, &r.Force, &r.Attempts); err != nil {
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

// DeferSymbolRequest keeps a request for another attempt after seconds,
// unless it was requested again since it was read.
func (s *Store) DeferSymbolRequest(r SymbolRequest, seconds int) error {
	_, err := s.DB.Exec(`
		UPDATE symbol_requests SET force = 0, attempts = attempts + 1,
		       not_before = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', ?)
		WHERE repo_id = ? AND seq = ?`, fmt.Sprintf("+%d seconds", seconds), r.RepoID, r.Seq)
	return err
}

const symbolIndexCols = `id, repo_id, commit_sha, tree, state, note, files, symbols, built_at`

func scanSymbolIndex(row *sql.Row) (SymbolIndex, error) {
	var x SymbolIndex
	err := row.Scan(&x.ID, &x.RepoID, &x.Commit, &x.Tree, &x.State, &x.Note, &x.Files, &x.Symbols, &x.BuiltAt)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// SymbolIndexFor returns a repository's current index, or ErrNotFound
// when none has been published.
func (s *Store) SymbolIndexFor(repoID int64) (SymbolIndex, error) {
	return scanSymbolIndex(s.DB.QueryRow(`SELECT `+symbolIndexCols+`
		FROM symbol_indexes WHERE repo_id = ? AND state IN ('ok', 'partial')`, repoID))
}

// SymbolIndexByID returns an index in any state.
func (s *Store) SymbolIndexByID(id int64) (SymbolIndex, error) {
	return scanSymbolIndex(s.DB.QueryRow(`SELECT `+symbolIndexCols+`
		FROM symbol_indexes WHERE id = ?`, id))
}

// BeginSymbolIndex creates an index in the building state, which no read
// sees until PublishSymbolIndex.
func (s *Store) BeginSymbolIndex(repoID int64, commit, tree string) (int64, error) {
	res, err := s.DB.Exec(`
		INSERT INTO symbol_indexes (repo_id, commit_sha, tree, state)
		VALUES (?, ?, ?, 'building')`, repoID, commit, tree)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// symbolInsertRows is how many rows one INSERT carries: eight columns
// each, well under SQLite's variable limit.
const symbolInsertRows = 200

// AddSymbols writes rows into an index being built, in one transaction.
// The caller keeps each call to a few thousand rows so the write lock is
// held briefly.
func (s *Store) AddSymbols(indexID int64, syms []SymbolRow) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
			args = append(args, indexID, r.Name, strings.ToLower(r.Name), r.Key, strings.ToLower(r.Key), r.Kind, r.Path, r.Line)
		}
		if _, err := tx.Exec(q.String(), args...); err != nil {
			return err
		}
		syms = syms[n:]
	}
	return tx.Commit()
}

// PublishSymbolIndex makes a built index the repository's current one, as
// ok or partial, and retires the one it replaces, in one short
// transaction. A recorded failure is cleared.
func (s *Store) PublishSymbolIndex(x SymbolIndex) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE symbol_indexes SET state = 'retired'
		WHERE repo_id = ? AND state IN ('ok', 'partial')`, x.RepoID); err != nil {
		return err
	}
	res, err := tx.Exec(`
		UPDATE symbol_indexes SET state = ?, note = ?, files = ?,
		       symbols = (SELECT COUNT(*) FROM symbols WHERE index_id = ?),
		       built_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ? AND repo_id = ? AND state = 'building'`,
		x.State, x.Note, x.Files, x.ID, x.ID, x.RepoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("symbol index %d is not being built", x.ID)
	}
	if _, err := tx.Exec("DELETE FROM symbol_failures WHERE repo_id = ?", x.RepoID); err != nil {
		return err
	}
	return tx.Commit()
}

// symbolDeleteRows is how many symbols one purge transaction deletes.
const symbolDeleteRows = 5000

// PurgeSymbolIndexes deletes a repository's indexes that are not current:
// retired ones, and building ones left by a run that did not finish. The
// symbols go a chunk per transaction, then the index row.
func (s *Store) PurgeSymbolIndexes(repoID int64) error {
	rows, err := s.DB.Query(`SELECT id FROM symbol_indexes
		WHERE repo_id = ? AND state IN ('building', 'retired')`, repoID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		for {
			res, err := s.DB.Exec(`DELETE FROM symbols WHERE id IN
				(SELECT id FROM symbols WHERE index_id = ? LIMIT ?)`, id, symbolDeleteRows)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				break
			}
		}
		if _, err := s.DB.Exec(`DELETE FROM symbol_indexes
			WHERE id = ? AND state IN ('building', 'retired')`, id); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceSymbolIndex builds, publishes and purges in one call: the
// worker's sequence, for a caller holding every row already.
func (s *Store) ReplaceSymbolIndex(x SymbolIndex, syms []SymbolRow) (int64, error) {
	id, err := s.BeginSymbolIndex(x.RepoID, x.Commit, x.Tree)
	if err != nil {
		return 0, err
	}
	for len(syms) > 0 {
		n := min(len(syms), 5000)
		if err := s.AddSymbols(id, syms[:n]); err != nil {
			return 0, err
		}
		syms = syms[n:]
	}
	x.ID = id
	if err := s.PublishSymbolIndex(x); err != nil {
		return 0, err
	}
	return id, s.PurgeSymbolIndexes(x.RepoID)
}

// RecordSymbolFailure records a run that could not build an index. The
// current index, if any, stays current.
func (s *Store) RecordSymbolFailure(repoID int64, tree, note string) error {
	_, err := s.DB.Exec(`
		INSERT INTO symbol_failures (repo_id, tree, note) VALUES (?, ?, ?)
		ON CONFLICT (repo_id) DO UPDATE SET tree = excluded.tree, note = excluded.note,
		       failed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, repoID, tree, note)
	return err
}

// SymbolFailureFor returns the last failure, or ErrNotFound.
func (s *Store) SymbolFailureFor(repoID int64) (SymbolFailure, error) {
	var f SymbolFailure
	err := s.DB.QueryRow(`SELECT tree, note, failed_at FROM symbol_failures WHERE repo_id = ?`, repoID).
		Scan(&f.Tree, &f.Note, &f.FailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// SymbolFailureRecent reports whether tree failed within the last
// seconds.
func (s *Store) SymbolFailureRecent(repoID int64, tree string, seconds int) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM symbol_failures
		WHERE repo_id = ? AND tree = ?
		  AND failed_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', ?)`,
		repoID, tree, fmt.Sprintf("-%d seconds", seconds)).Scan(&n)
	return n > 0, err
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
