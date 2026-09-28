package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Audit appends to the security feed. Events are the product feed; this
// records who did what, from where, for an operator. actorID 0 means the
// host admin (gitbayd admin commands) or an unauthenticated source.
func (s *Store) Audit(actorID int64, action string, data map[string]any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	id, createdAt, hash, err := s.appendAudit(actorID, action, string(raw))
	if s.AuditJournal == nil {
		return
	}
	if err != nil {
		s.AuditJournal.Error("audit: append", "action", action, "err", err)
		return
	}
	s.AuditJournal.Info("audit", "id", id, "actor", actorID, "action", action,
		"data", string(raw), "created_at", createdAt, "hash", hash)
}

// appendAudit writes one row and its chain hash in one transaction. The
// store begins every transaction IMMEDIATE, so two writers — the daemon
// and a gitbayd admin command, say — cannot both read the same last
// hash. The timestamp is taken once the write lock is held, so created_at
// rises with id unless the clock steps back.
func (s *Store) appendAudit(actorID int64, action, data string) (int64, string, string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, "", "", err
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow("SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1").Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", err
	}
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	createdAt := fmtTime(time.Now())
	res, err := tx.Exec(
		"INSERT INTO audit_log (actor_id, actor_ref, action, data_json, created_at, prev_hash) VALUES (?, ?, ?, ?, ?, ?)",
		actor, actorID, action, data, createdAt, prev)
	if err != nil {
		return 0, "", "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", "", err
	}
	hash := auditHash(prev, id, actorID, action, createdAt, data)
	if _, err := tx.Exec("UPDATE audit_log SET hash = ? WHERE id = ?", hash, id); err != nil {
		return 0, "", "", err
	}
	return id, createdAt, hash, tx.Commit()
}

// auditHash covers every column an operator reads, plus the previous
// row's hash. A JSON array keeps field boundaries unambiguous.
func auditHash(prev string, id, actor int64, action, createdAt, data string) string {
	b, _ := json.Marshal([]any{prev, id, actor, action, createdAt, data})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AuditChain is what VerifyAuditChain found.
type AuditChain struct {
	Rows      int   // rows read
	Unchained int   // rows from before migration 0064, which carry no hash
	First     int64 // first chained row; with no unchained rows before it, its prev_hash is taken as given, since retention may have removed the row it names
	Last      int64
	LastHash  string
	BrokenAt  int64 // 0 when the chain is intact
	Reason    string
}

// VerifyAuditChain recomputes every row's hash in id order and stops at
// the first row that does not match. Rows removed from the end of the
// table cannot be detected from the database, nor can new rows written
// after that under the reused ids (id is not AUTOINCREMENT); the journal
// copy is the record that shows either.
func (s *Store) VerifyAuditChain() (AuditChain, error) {
	rows, err := s.DB.Query(`SELECT id, actor_id, actor_ref, action, data_json, created_at, prev_hash, hash
		FROM audit_log ORDER BY id`)
	if err != nil {
		return AuditChain{}, err
	}
	defer rows.Close()
	var res AuditChain
	for rows.Next() {
		var (
			id, actor                           int64
			actorID                             sql.NullInt64
			action, data, createdAt, prev, hash string
		)
		if err := rows.Scan(&id, &actorID, &actor, &action, &data, &createdAt, &prev, &hash); err != nil {
			return res, err
		}
		res.Rows++
		switch {
		case hash == "" && res.First == 0:
			res.Unchained++
			continue
		case hash == "":
			res.BrokenAt, res.Reason = id, "row has no hash after the chain began"
		// The first row appended after migration 0064 names the last
		// unchained row's empty hash. Retention removes the oldest rows
		// first, so unchained rows before a chained one with a non-empty
		// prev_hash had their hashes blanked.
		case res.First == 0 && res.Unchained > 0 && prev != "":
			res.BrokenAt, res.Reason = id, "chained rows before it lost their hashes"
		case res.First != 0 && prev != res.LastHash:
			res.BrokenAt, res.Reason = id, "previous hash does not match: a row before it was removed or changed"
		case auditHash(prev, id, actor, action, createdAt, data) != hash:
			res.BrokenAt, res.Reason = id, "row contents do not match its hash"
		// actor_id is not hashed; it may only be the actor_ref written
		// with the row, or NULL once that account is deleted.
		case actorID.Valid && actorID.Int64 != actor:
			res.BrokenAt, res.Reason = id, "actor_id does not match the actor the row was written with"
		}
		if res.BrokenAt != 0 {
			return res, nil
		}
		if res.First == 0 {
			res.First = id
		}
		res.Last, res.LastHash = id, hash
	}
	return res, rows.Err()
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
