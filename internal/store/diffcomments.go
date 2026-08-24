package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type DiffComment struct {
	ID         int64
	Author     string
	HeadSHA    string
	Path       string
	Side       string
	Line       int64
	Body       string
	ReplyTo    int64 // 0 for thread roots
	ResolvedBy string
	CreatedAt  string
}

// AddDiffComment creates a thread root (replyTo 0) or a reply. Replies
// inherit the root's anchor and must belong to the same MR.
func (s *Store) AddDiffComment(mrID, authorID int64, headSHA, path, side string, line int64, body string, replyTo int64) (int64, error) {
	if replyTo != 0 {
		var rootMR int64
		var rootReply sql.NullInt64
		err := s.DB.QueryRow(
			"SELECT mr_id, reply_to FROM mr_diff_comments WHERE id = ?", replyTo).Scan(&rootMR, &rootReply)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("no thread %d: %w", replyTo, ErrNotFound)
		}
		if err != nil {
			return 0, err
		}
		if rootMR != mrID {
			return 0, fmt.Errorf("thread %d belongs to a different merge request", replyTo)
		}
		if rootReply.Valid {
			return 0, fmt.Errorf("reply to the thread root %d, not to a reply", rootReply.Int64)
		}
		err = s.DB.QueryRow(
			"SELECT head_sha, path, side, line FROM mr_diff_comments WHERE id = ?", replyTo).
			Scan(&headSHA, &path, &side, &line)
		if err != nil {
			return 0, err
		}
	}
	var reply any
	if replyTo != 0 {
		reply = replyTo
	}
	res, err := s.DB.Exec(`
		INSERT INTO mr_diff_comments (mr_id, author_id, head_sha, path, side, line, body, reply_to)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mrID, authorID, headSHA, path, side, line, body, reply)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListDiffComments returns every diff comment on an MR, roots and replies,
// oldest first.
func (s *Store) ListDiffComments(mrID int64) ([]DiffComment, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, u.username, c.head_sha, c.path, c.side, c.line, c.body,
		       COALESCE(c.reply_to, 0), COALESCE(r.username, ''), c.created_at
		FROM mr_diff_comments c
		JOIN users u ON u.id = c.author_id
		LEFT JOIN users r ON r.id = c.resolved_by
		WHERE c.mr_id = ? ORDER BY c.id`, mrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiffComment
	for rows.Next() {
		var c DiffComment
		if err := rows.Scan(&c.ID, &c.Author, &c.HeadSHA, &c.Path, &c.Side, &c.Line, &c.Body,
			&c.ReplyTo, &c.ResolvedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetThreadResolved resolves or unresolves a thread root.
func (s *Store) SetThreadResolved(mrID, rootID, byUser int64, resolved bool) error {
	var q string
	var args []any
	if resolved {
		q = `UPDATE mr_diff_comments SET resolved_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), resolved_by = ?
		     WHERE id = ? AND mr_id = ? AND reply_to IS NULL`
		args = []any{byUser, rootID, mrID}
	} else {
		q = `UPDATE mr_diff_comments SET resolved_at = NULL, resolved_by = NULL
		     WHERE id = ? AND mr_id = ? AND reply_to IS NULL`
		args = []any{rootID, mrID}
	}
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DiffCommentAuthor returns the author id of one comment.
func (s *Store) DiffCommentAuthor(mrID, id int64) (int64, error) {
	var author int64
	err := s.DB.QueryRow(
		"SELECT author_id FROM mr_diff_comments WHERE id = ? AND mr_id = ?", id, mrID).Scan(&author)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return author, err
}

// UnresolvedThreadCount counts unresolved thread roots on an MR.
func (s *Store) UnresolvedThreadCount(mrID int64) (int, error) {
	var n int
	err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM mr_diff_comments WHERE mr_id = ? AND reply_to IS NULL AND resolved_at IS NULL",
		mrID).Scan(&n)
	return n, err
}
