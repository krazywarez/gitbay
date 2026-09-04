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
	// Pending marks a comment in a review its author has not submitted.
	// Only they can see it, and `mr review` publishes it.
	Pending   bool
	CreatedAt string
}

// AddDiffComment creates a thread root (replyTo 0) or a reply. Replies
// inherit the root's anchor and must belong to the same MR.
func (s *Store) AddDiffComment(mrID, authorID int64, headSHA, path, side string, line int64, body string, replyTo int64, pending bool) (int64, error) {
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
		INSERT INTO mr_diff_comments (mr_id, author_id, head_sha, path, side, line, body, reply_to, pending)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mrID, authorID, headSHA, path, side, line, body, reply, pending)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListDiffComments returns the diff comments on an MR that viewer may
// see: everything published, plus their own pending ones. viewer 0 is an
// anonymous reader, who sees only what is published.
func (s *Store) ListDiffComments(mrID, viewer int64) ([]DiffComment, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, u.username, c.head_sha, c.path, c.side, c.line, c.body,
		       COALESCE(c.reply_to, 0), COALESCE(r.username, ''), c.pending, c.created_at
		FROM mr_diff_comments c
		JOIN users u ON u.id = c.author_id
		LEFT JOIN users r ON r.id = c.resolved_by
		WHERE c.mr_id = ?1 AND (c.pending = 0 OR c.author_id = ?2)
		ORDER BY c.id`, mrID, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiffComment
	for rows.Next() {
		var c DiffComment
		if err := rows.Scan(&c.ID, &c.Author, &c.HeadSHA, &c.Path, &c.Side, &c.Line, &c.Body,
			&c.ReplyTo, &c.ResolvedBy, &c.Pending, &c.CreatedAt); err != nil {
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
//
// Pending roots are excluded: an unsubmitted comment is one reviewer's
// note to themselves, and blocking a merge on it would let anyone stall
// a merge request with a thread nobody else can see or resolve.
func (s *Store) UnresolvedThreadCount(mrID int64) (int, error) {
	var n int
	err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM mr_diff_comments
		WHERE mr_id = ? AND reply_to IS NULL AND resolved_at IS NULL AND pending = 0`,
		mrID).Scan(&n)
	return n, err
}

// PublishPendingComments makes an author's pending comments on an MR
// visible, and reports how many. This is what `mr review` does with the
// batch the reviewer composed.
func (s *Store) PublishPendingComments(mrID, authorID int64) (int64, error) {
	res, err := s.DB.Exec(
		"UPDATE mr_diff_comments SET pending = 0 WHERE mr_id = ? AND author_id = ? AND pending = 1",
		mrID, authorID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DiscardPendingComments deletes an author's unsubmitted comments. Only
// pending rows: a published comment is part of the conversation and is
// not something its author can quietly take back.
func (s *Store) DiscardPendingComments(mrID, authorID int64) (int64, error) {
	res, err := s.DB.Exec(
		"DELETE FROM mr_diff_comments WHERE mr_id = ? AND author_id = ? AND pending = 1",
		mrID, authorID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountPendingComments is how many unsubmitted comments an author holds
// on an MR, for the reminder that they have a review in progress.
func (s *Store) CountPendingComments(mrID, authorID int64) int {
	var n int
	s.DB.QueryRow(
		"SELECT COUNT(*) FROM mr_diff_comments WHERE mr_id = ? AND author_id = ? AND pending = 1",
		mrID, authorID).Scan(&n)
	return n
}
