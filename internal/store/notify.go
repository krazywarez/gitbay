package store

import "time"

type QueuedMail struct {
	ID        int64
	Recipient string
	Subject   string
	Body      string
	Attempts  int
}

func (s *Store) EnqueueMail(recipient, subject, body string) error {
	_, err := s.DB.Exec(
		"INSERT INTO notifications (recipient, subject, body) VALUES (?, ?, ?)",
		recipient, subject, body)
	return err
}

func (s *Store) DueMail(limit int) ([]QueuedMail, error) {
	rows, err := s.DB.Query(`
		SELECT id, recipient, subject, body, attempts FROM notifications
		WHERE sent_at IS NULL AND failed_at IS NULL
		  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id LIMIT ?`, fmtTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedMail
	for rows.Next() {
		var m QueuedMail
		if err := rows.Scan(&m.ID, &m.Recipient, &m.Subject, &m.Body, &m.Attempts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkMailSent(id int64) error {
	_, err := s.DB.Exec(
		"UPDATE notifications SET sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1 WHERE id = ?", id)
	return err
}

func (s *Store) MarkMailFailed(id int64, errMsg string, nextAt *time.Time) error {
	if nextAt == nil {
		_, err := s.DB.Exec(
			"UPDATE notifications SET failed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1, last_error = ? WHERE id = ?",
			errMsg, id)
		return err
	}
	_, err := s.DB.Exec(
		"UPDATE notifications SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?",
		errMsg, fmtTime(*nextAt), id)
	return err
}

// IssueParticipants returns distinct user ids involved in an issue: the
// author and every commenter.
func (s *Store) IssueParticipants(issueID int64) ([]int64, error) {
	return s.idQuery(`
		SELECT author_id FROM issues WHERE id = ?
		UNION SELECT author_id FROM issue_comments WHERE issue_id = ?`, issueID, issueID)
}

// MRParticipants returns distinct user ids involved in an MR: author,
// commenters, reviewers.
func (s *Store) MRParticipants(mrID int64) ([]int64, error) {
	return s.idQuery(`
		SELECT author_id FROM merge_requests WHERE id = ?
		UNION SELECT author_id FROM mr_comments WHERE mr_id = ?
		UNION SELECT reviewer_id FROM mr_reviews WHERE mr_id = ?`, mrID, mrID, mrID)
}

// RepoNotifyTargets returns who should hear about new activity on a repo:
// the owning user, or every admin of the owning org.
func (s *Store) RepoNotifyTargets(repo Repo) ([]int64, error) {
	if repo.OwnerKind == "user" {
		return []int64{repo.OwnerID}, nil
	}
	return s.idQuery(
		"SELECT user_id FROM org_members WHERE org_id = ? AND role = 'admin'", repo.OwnerID)
}

func (s *Store) idQuery(q string, args ...any) ([]int64, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
