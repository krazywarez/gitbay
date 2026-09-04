package store

import (
	"strconv"
	"strings"
)

// Notice is one inbox row: what happened, where, and whether it has been
// read. Path is the web path without a leading slash, so a client turns it
// into a link by prefixing the site URL.
type Notice struct {
	ID        int64
	RepoPath  string
	Kind      string
	Actor     string
	Summary   string
	Path      string
	CreatedAt string
	ReadAt    string
}

// inboxSelect resolves the repository path the same way the dashboard
// queries do, since repos.owner_id is polymorphic over users and orgs.
const inboxSelect = `
	SELECT n.id, COALESCE(u.username, o.name) || '/' || r.name,
	       n.kind, n.actor, n.summary, n.path, n.created_at, COALESCE(n.read_at, '')
	FROM inbox n
	JOIN repos r ON r.id = n.repo_id
	LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
	LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
	WHERE n.user_id = ?1`

// AddNotice files one inbox row. Best-effort like the mail it accompanies:
// the action it reports has already succeeded.
func (s *Store) AddNotice(userID, repoID int64, kind, actor, summary, path string) error {
	_, err := s.DB.Exec(`
		INSERT INTO inbox (user_id, repo_id, kind, actor, summary, path)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, repoID, kind, actor, summary, path)
	return err
}

// Inbox returns a user's notices, newest first. unreadOnly drops what has
// been read; afterID pages backwards from a previous page's last row.
func (s *Store) Inbox(userID int64, unreadOnly bool, limit int, afterID int64) ([]Notice, error) {
	q, args := inboxSelect, []any{userID}
	if unreadOnly {
		q += " AND n.read_at IS NULL"
	}
	if afterID > 0 {
		q += " AND n.id < ?2"
		args = append(args, afterID)
	}
	q += " ORDER BY n.id DESC"
	if limit > 0 {
		q += " LIMIT ?" + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notice
	for rows.Next() {
		var n Notice
		if err := rows.Scan(&n.ID, &n.RepoPath, &n.Kind, &n.Actor, &n.Summary,
			&n.Path, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotices counts what the badge shows.
func (s *Store) UnreadNotices(userID int64) int {
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM inbox WHERE user_id = ? AND read_at IS NULL",
		userID).Scan(&n)
	return n
}

// MarkNoticesRead marks the given ids read, or every unread notice when
// ids is empty. It returns how many rows changed. Ids belonging to another
// user match nothing, so one user cannot touch another's inbox.
func (s *Store) MarkNoticesRead(userID int64, ids []int64) (int64, error) {
	const set = "UPDATE inbox SET read_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE user_id = ? AND read_at IS NULL"
	args := []any{userID}
	q := set
	if len(ids) > 0 {
		q += " AND id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetRepoWatch records an explicit watch or mute. Re-running it with the
// other state replaces the row.
func (s *Store) SetRepoWatch(repoID, userID int64, state string) error {
	_, err := s.DB.Exec(`
		INSERT INTO repo_watchers (repo_id, user_id, state) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, user_id) DO UPDATE SET state = excluded.state`,
		repoID, userID, state)
	return err
}

// RepoWatchState returns "watching", "muted", or "" for the default.
func (s *Store) RepoWatchState(repoID, userID int64) string {
	var state string
	s.DB.QueryRow("SELECT state FROM repo_watchers WHERE repo_id = ? AND user_id = ?",
		repoID, userID).Scan(&state)
	return state
}

// NotifyRecipients is who actually hears about something on a repository:
// the callers targets — owners, or a thread's participants — widened by
// the repository's watchers, minus the actor and minus anyone who muted
// it. Muting wins over every other reason to be told, including owning
// the repository or having written the thread.
func (s *Store) NotifyRecipients(repoID, actorID int64, targets []int64) ([]int64, error) {
	rows, err := s.DB.Query("SELECT user_id, state FROM repo_watchers WHERE repo_id = ?", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skip := map[int64]bool{actorID: true}
	var watching []int64
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		if state == "muted" {
			skip[id] = true
		} else {
			watching = append(watching, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []int64
	seen := map[int64]bool{}
	for _, id := range append(append([]int64{}, targets...), watching...) {
		if skip[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}
