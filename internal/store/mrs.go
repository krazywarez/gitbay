package store

import (
	"database/sql"
	"errors"
	"strings"
)

type MR struct {
	ID           int64
	RepoID       int64
	Number       int64
	Author       string
	SourceRepoID int64  // 0 when the source repo is gone
	SourcePath   string // owner/name of source repo, "" when gone
	SourceRef    string
	TargetRef    string
	Title        string
	Body         string
	BodyFormat   string // md | org
	State        string // open | merged | closed | source_gone
	Milestone    string
	HeadSHA      string
	MergedBase   string // target tip at merge time; base for historical diffs
	MergedAt     string // "" unless merged
	MergedBy     string // "" when unknown (imports) or the account is gone
	ClosedAt     string // "" unless closed without merging
	ClosedBy     string
	CreatedAt    string
	UpdatedAt    string
}

type MRReview struct {
	Reviewer  string
	Verdict   string
	HeadSHA   string
	Stale     bool
	CreatedAt string
}

func (s *Store) CreateMR(repoID, authorID, sourceRepoID int64, sourceRef, targetRef, title, body, headSHA, format string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE repos SET mr_counter = mr_counter + 1 WHERE id = ?", repoID); err != nil {
		return 0, err
	}
	var n int64
	if err := tx.QueryRow("SELECT mr_counter FROM repos WHERE id = ?", repoID).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT INTO merge_requests (repo_id, number, author_id, source_repo_id, source_ref, target_ref, title, body, head_sha, body_format)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID, n, authorID, sourceRepoID, sourceRef, targetRef, title, body, headSHA, format); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

const mrSelect = `
	SELECT m.id, m.repo_id, m.number, u.username,
	       COALESCE(m.source_repo_id, 0),
	       COALESCE(COALESCE(su.username, so.name) || '/' || sr.name, ''),
	       m.source_ref, m.target_ref, m.title, m.body, m.body_format, m.state,
	       COALESCE(ms.title, ''), m.head_sha,
	       m.merged_base, m.merged_at, COALESCE(mu.username, ''),
	       m.closed_at, COALESCE(cu.username, ''), m.created_at, m.updated_at
	FROM merge_requests m
	JOIN users u ON u.id = m.author_id
	LEFT JOIN users mu ON mu.id = m.merged_by
	LEFT JOIN users cu ON cu.id = m.closed_by
	LEFT JOIN repos sr ON sr.id = m.source_repo_id
	LEFT JOIN users su ON sr.owner_kind = 'user' AND su.id = sr.owner_id
	LEFT JOIN orgs so  ON sr.owner_kind = 'org'  AND so.id = sr.owner_id
	LEFT JOIN milestones ms ON ms.id = m.milestone_id`

func scanMR(row interface{ Scan(...any) error }) (MR, error) {
	var m MR
	err := row.Scan(&m.ID, &m.RepoID, &m.Number, &m.Author, &m.SourceRepoID, &m.SourcePath,
		&m.SourceRef, &m.TargetRef, &m.Title, &m.Body, &m.BodyFormat, &m.State, &m.Milestone, &m.HeadSHA, &m.MergedBase,
		&m.MergedAt, &m.MergedBy, &m.ClosedAt, &m.ClosedBy, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (s *Store) MRByNumber(repoID, number int64) (MR, error) {
	m, err := scanMR(s.DB.QueryRow(mrSelect+" WHERE m.repo_id = ? AND m.number = ?", repoID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// ListMRs returns merge requests for a repo. limit 0 means everything;
// before (an MR number) starts the page strictly below it, matching the
// number-descending order.
func (s *Store) ListMRs(repoID int64, state string, limit int, before int64) ([]MR, error) {
	q := mrSelect + " WHERE m.repo_id = ?"
	args := []any{repoID}
	if state != "all" {
		q += " AND m.state = ?"
		args = append(args, state)
	}
	if before > 0 {
		q += " AND m.number < ?"
		args = append(args, before)
	}
	q += " ORDER BY m.number DESC"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MR
	for rows.Next() {
		m, err := scanMR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenMRsBySource returns open (and source_gone) MRs fed by the given source
// repo branch — the cross-repo hook effect consults this.
func (s *Store) OpenMRsBySource(sourceRepoID int64, sourceRef string) ([]MR, error) {
	rows, err := s.DB.Query(
		mrSelect+" WHERE m.source_repo_id = ? AND m.source_ref = ? AND m.state IN ('open','source_gone')",
		sourceRepoID, sourceRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MR
	for rows.Next() {
		m, err := scanMR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkMerged records the merge along with the target tip it landed on, so
// the MR's diff stays reconstructable after fast-forwards. actorID 0 and an
// empty at leave the merger unknown and stamp the current time, which is
// what an import that carries neither can say.
func (s *Store) MarkMerged(mrID int64, baseSHA string, actorID int64, at string) error {
	_, err := s.DB.Exec(
		`UPDATE merge_requests SET state = 'merged', merged_base = ?,
			merged_at = COALESCE(NULLIF(?, ''), strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			merged_by = NULLIF(?, 0),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		baseSHA, at, actorID, mrID)
	return err
}

// MarkClosed is MarkMerged's counterpart for a merge request closed without
// merging.
func (s *Store) MarkClosed(mrID, actorID int64, at string) error {
	_, err := s.DB.Exec(
		`UPDATE merge_requests SET state = 'closed',
			closed_at = COALESCE(NULLIF(?, ''), strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			closed_by = NULLIF(?, 0),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		at, actorID, mrID)
	return err
}

// SetMRState moves an MR between states that carry no resolution stamp.
// Returning to open (a source branch that came back) clears one.
func (s *Store) SetMRState(mrID int64, state string) error {
	stamp := ""
	if state == "open" || state == "source_gone" {
		stamp = ", merged_at = '', merged_by = NULL, closed_at = '', closed_by = NULL"
	}
	res, err := s.DB.Exec(
		"UPDATE merge_requests SET state = ?"+stamp+", updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		state, mrID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMRHead records a new head and marks every review at another head
// stale, in one transaction.
func (s *Store) UpdateMRHead(mrID int64, headSHA string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"UPDATE merge_requests SET head_sha = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		headSHA, mrID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE mr_reviews SET stale = 1 WHERE mr_id = ? AND head_sha <> ?", mrID, headSHA); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMRTarget retargets a merge request and marks every existing review
// stale, in one transaction. The base of the diff is derived from the
// target on every read, so nothing else has to move; an approval,
// though, was of the diff against the old branch.
func (s *Store) SetMRTarget(mrID int64, targetRef string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"UPDATE merge_requests SET target_ref = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		targetRef, mrID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec("UPDATE mr_reviews SET stale = 1 WHERE mr_id = ?", mrID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSourceGoneForRepo flags every open MR sourced from the repo; called
// when a fork is deleted. Head refs in the target repos are retained.
func (s *Store) MarkSourceGoneForRepo(sourceRepoID int64) error {
	_, err := s.DB.Exec(
		"UPDATE merge_requests SET state = 'source_gone', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE source_repo_id = ? AND state = 'open'",
		sourceRepoID)
	return err
}

func (s *Store) AddMRComment(mrID, authorID int64, body, format string) error {
	_, err := s.DB.Exec(
		"INSERT INTO mr_comments (mr_id, author_id, body, body_format) VALUES (?, ?, ?, ?)",
		mrID, authorID, body, format)
	return err
}

// UpdateMRText edits title, body, and/or markup format; nil leaves a field
// unchanged.
func (s *Store) UpdateMRText(mrID int64, title, body, format *string) error {
	set, args := []string{}, []any{}
	if title != nil {
		set, args = append(set, "title = ?"), append(args, *title)
	}
	if body != nil {
		set, args = append(set, "body = ?"), append(args, *body)
	}
	if format != nil {
		set, args = append(set, "body_format = ?"), append(args, *format)
	}
	if len(set) == 0 {
		return nil
	}
	set = append(set, "updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')")
	args = append(args, mrID)
	res, err := s.DB.Exec("UPDATE merge_requests SET "+strings.Join(set, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMRSystemComment is the informational counterpart of AddMRComment.
func (s *Store) AddMRSystemComment(mrID, actorID int64, body string) error {
	_, err := s.DB.Exec(
		"INSERT INTO mr_comments (mr_id, author_id, body, kind) VALUES (?, ?, ?, 'system')",
		mrID, actorID, body)
	return err
}

func (s *Store) ListMRComments(mrID int64) ([]IssueComment, error) {
	rows, err := s.DB.Query(`
		SELECT CASE WHEN c.kind = 'system' THEN 'system' ELSE u.username END,
		       c.body, c.body_format, c.created_at, c.kind
		FROM mr_comments c JOIN users u ON u.id = c.author_id
		WHERE c.mr_id = ? ORDER BY c.id`, mrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssueComment
	for rows.Next() {
		var c IssueComment
		if err := rows.Scan(&c.Author, &c.Body, &c.BodyFormat, &c.CreatedAt, &c.Kind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddMRReview(mrID, reviewerID int64, verdict, headSHA string) error {
	_, err := s.DB.Exec(
		"INSERT INTO mr_reviews (mr_id, reviewer_id, verdict, head_sha) VALUES (?, ?, ?, ?)",
		mrID, reviewerID, verdict, headSHA)
	return err
}

func (s *Store) ListMRReviews(mrID int64) ([]MRReview, error) {
	rows, err := s.DB.Query(`
		SELECT u.username, r.verdict, r.head_sha, r.stale, r.created_at
		FROM mr_reviews r JOIN users u ON u.id = r.reviewer_id
		WHERE r.mr_id = ? ORDER BY r.id`, mrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MRReview
	for rows.Next() {
		var r MRReview
		var stale int
		if err := rows.Scan(&r.Reviewer, &r.Verdict, &r.HeadSHA, &stale, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Stale = stale != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// PrimaryVerifiedEmail returns the user's primary email if verified, else "".
func (s *Store) PrimaryVerifiedEmail(userID int64) (string, error) {
	var addr string
	err := s.DB.QueryRow(
		"SELECT address FROM emails WHERE user_id = ? AND is_primary = 1 AND verified_at IS NOT NULL",
		userID).Scan(&addr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return addr, err
}
