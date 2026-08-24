package store

import "errors"

// ErrExists marks unique-constraint refusals callers turn into messages.
var ErrExists = errors.New("already exists")

// Mirror propagates refs to (push) or from (pull) a foreign remote. The
// token is stored server-side — unlike import, mirroring is recurring —
// and must never be echoed back in listings.
type Mirror struct {
	ID        int64
	RepoID    int64
	Direction string // push | pull
	URL       string
	Username  string
	Token     string
	Dirty     bool
	LastSync  string
	LastError string
}

func (s *Store) AddMirror(repoID int64, direction, url, username, token string) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO mirrors (repo_id, direction, url, username, token) VALUES (?, ?, ?, ?, ?)",
		repoID, direction, url, username, token)
	if err != nil {
		if isUniqueErr(err) {
			return 0, ErrExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

const mirrorSelect = `
	SELECT id, repo_id, direction, url, username, token, dirty, last_sync, last_error
	FROM mirrors`

func scanMirror(row interface{ Scan(...any) error }) (Mirror, error) {
	var m Mirror
	err := row.Scan(&m.ID, &m.RepoID, &m.Direction, &m.URL, &m.Username, &m.Token,
		&m.Dirty, &m.LastSync, &m.LastError)
	return m, err
}

func (s *Store) mirrorQuery(q string, args ...any) ([]Mirror, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mirror
	for rows.Next() {
		m, err := scanMirror(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ListMirrors(repoID int64) ([]Mirror, error) {
	return s.mirrorQuery(mirrorSelect+" WHERE repo_id = ? ORDER BY id", repoID)
}

// DueMirrors returns mirrors needing a sync: anything dirty, plus pull
// mirrors whose last sync is older than intervalSeconds.
func (s *Store) DueMirrors(intervalSeconds int) ([]Mirror, error) {
	return s.mirrorQuery(mirrorSelect+`
		WHERE dirty = 1
		   OR (direction = 'pull' AND (last_sync = ''
		       OR strftime('%s','now') - strftime('%s', last_sync) > ?))
		ORDER BY id`, intervalSeconds)
}

func (s *Store) RemoveMirror(repoID, id int64) error {
	res, err := s.DB.Exec("DELETE FROM mirrors WHERE repo_id = ? AND id = ?", repoID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkMirrorsDirty schedules a sync. An empty direction marks both.
func (s *Store) MarkMirrorsDirty(repoID int64, direction string) error {
	q := "UPDATE mirrors SET dirty = 1 WHERE repo_id = ?"
	args := []any{repoID}
	if direction != "" {
		q += " AND direction = ?"
		args = append(args, direction)
	}
	_, err := s.DB.Exec(q, args...)
	return err
}

// SetMirrorResult records a sync outcome and clears the dirty flag.
func (s *Store) SetMirrorResult(id int64, syncErr string) error {
	_, err := s.DB.Exec(`
		UPDATE mirrors SET dirty = 0, last_error = ?,
		       last_sync = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`, syncErr, id)
	return err
}

// PullMirrored reports whether the repo has a pull mirror, which makes it
// read-only locally: its refs belong to the upstream.
func (s *Store) PullMirrored(repoID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM mirrors WHERE repo_id = ? AND direction = 'pull'", repoID).Scan(&n)
	return n > 0, err
}
