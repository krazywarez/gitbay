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

// AuditFilter narrows AuditEntries. Actor is a username, or "-" for rows
// with no actor (host commands, auth failures). ActionPrefix matches the
// start of the action. Since is an ISO timestamp in the log's own format.
type AuditFilter struct {
	Actor        string
	ActionPrefix string
	Since        string
	Limit        int
}

func (s *Store) AuditEntries(f AuditFilter) ([]AuditEntry, error) {
	q := `SELECT a.id, COALESCE(u.username, ''), a.action, a.data_json, a.created_at
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id WHERE 1 = 1`
	var args []any
	switch f.Actor {
	case "":
	case "-":
		q += " AND a.actor_id IS NULL"
	default:
		q += " AND u.username = ?"
		args = append(args, f.Actor)
	}
	if f.ActionPrefix != "" {
		q += " AND substr(a.action, 1, length(?)) = ?"
		args = append(args, f.ActionPrefix, f.ActionPrefix)
	}
	if f.Since != "" {
		q += " AND a.created_at >= ?"
		args = append(args, f.Since)
	}
	q += " ORDER BY a.id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.DB.Query(q, args...)
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
