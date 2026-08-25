package store

import (
	"fmt"
	"regexp"
)

// commitRefPat matches the legacy commit-reference comment format, written
// before these entries were system messages: "closed by commit <sha>: …"
// or "referenced in commit <sha>: …" with a bare short sha.
var commitRefPat = regexp.MustCompile(`^((?:closed by|referenced in) commit )([0-9a-f]{7,40})(: [\s\S]*)$`)

// MigrateCommitRefComments converts legacy commit-reference comments into
// system messages with a linked sha, matching what new references produce.
// It is idempotent: only kind='comment' rows are considered, and converted
// rows become kind='system'. Returns the number converted.
func (s *Store) MigrateCommitRefComments() (int, error) {
	n1, err := s.migrateRefTable(
		"issue_comments", "issues", "issue_id",
		"'closed by commit %' OR c.body LIKE 'referenced in commit %'")
	if err != nil {
		return n1, err
	}
	n2, err := s.migrateRefTable(
		"mr_comments", "merge_requests", "mr_id",
		"'closed by commit %' OR c.body LIKE 'referenced in commit %'")
	return n1 + n2, err
}

func (s *Store) migrateRefTable(table, itemTable, fk, likeClause string) (int, error) {
	q := fmt.Sprintf(`
		SELECT c.id, c.body, COALESCE(u.username, o.name) || '/' || r.name
		FROM %s c
		JOIN %s it ON it.id = c.%s
		JOIN repos r ON r.id = it.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs  o ON r.owner_kind = 'org'  AND o.id = r.owner_id
		WHERE c.kind = 'comment' AND (c.body LIKE %s)`, table, itemTable, fk, likeClause)
	rows, err := s.DB.Query(q)
	if err != nil {
		return 0, err
	}
	type conv struct {
		id   int64
		body string
	}
	var todo []conv
	for rows.Next() {
		var id int64
		var body, path string
		if err := rows.Scan(&id, &body, &path); err != nil {
			rows.Close()
			return 0, err
		}
		m := commitRefPat.FindStringSubmatch(body)
		if m == nil {
			continue // LIKE candidate that isn't the exact format
		}
		verb, sha, rest := m[1], m[2], m[3]
		newBody := fmt.Sprintf("%s[%s](/%s/commit/%s)%s", verb, sha, path, sha, rest)
		todo = append(todo, conv{id, newBody})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	upd := fmt.Sprintf("UPDATE %s SET body = ?, kind = 'system' WHERE id = ?", table)
	for _, c := range todo {
		if _, err := tx.Exec(upd, c.body, c.id); err != nil {
			return 0, err
		}
	}
	return len(todo), tx.Commit()
}
