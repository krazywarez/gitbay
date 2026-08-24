package store

import "encoding/json"

// Audit appends to the security feed. Events are the product feed; this
// records who did what, from where, for an operator. actorID 0 means the
// host admin (gitbayd admin commands) or an unauthenticated source.
func (s *Store) Audit(actorID int64, action string, data map[string]any) {
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	s.DB.Exec("INSERT INTO audit_log (actor_id, action, data_json) VALUES (?, ?, ?)",
		actor, action, string(raw))
}

type AuditEntry struct {
	ID        int64  `json:"id"`
	Actor     string `json:"actor,omitempty"`
	Action    string `json:"action"`
	Data      string `json:"data"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) AuditEntries(limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(`
		SELECT a.id, COALESCE(u.username, ''), a.action, a.data_json, a.created_at
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id
		ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
