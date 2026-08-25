package store

type CommitStatus struct {
	Context     string
	State       string // pending | success | failure | error
	Description string
	TargetURL   string
	Creator     string
	UpdatedAt   string
}

// SetCommitStatus upserts the latest state for one context on one commit.
// A zero creatorID records no creator (system actions like the scheduler).
func (s *Store) SetCommitStatus(repoID int64, sha, context, state, description, targetURL string, creatorID int64) error {
	var creator any
	if creatorID != 0 {
		creator = creatorID
	}
	_, err := s.DB.Exec(`
		INSERT INTO commit_statuses (repo_id, commit_sha, context, state, description, target_url, creator_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, commit_sha, context) DO UPDATE SET
			state = excluded.state, description = excluded.description,
			target_url = excluded.target_url, creator_id = excluded.creator_id,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		repoID, sha, context, state, description, targetURL, creator)
	return err
}

// ListCommitStatuses returns the latest status per context for a commit.
func (s *Store) ListCommitStatuses(repoID int64, sha string) ([]CommitStatus, error) {
	rows, err := s.DB.Query(`
		SELECT cs.context, cs.state, cs.description, cs.target_url, COALESCE(u.username, ''), cs.updated_at
		FROM commit_statuses cs LEFT JOIN users u ON u.id = cs.creator_id
		WHERE cs.repo_id = ? AND cs.commit_sha = ? ORDER BY cs.context`, repoID, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommitStatus
	for rows.Next() {
		var c CommitStatus
		if err := rows.Scan(&c.Context, &c.State, &c.Description, &c.TargetURL, &c.Creator, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CombinedStatus reduces per-context states to one: error/failure dominate,
// then pending, then success; "" when no statuses exist.
func CombinedStatus(statuses []CommitStatus) string {
	if len(statuses) == 0 {
		return ""
	}
	combined := "success"
	for _, s := range statuses {
		switch s.State {
		case "error", "failure":
			return "failure"
		case "pending":
			combined = "pending"
		}
	}
	return combined
}
