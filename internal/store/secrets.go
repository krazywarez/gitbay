package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"gitbay.org/gitbay/internal/seal"
)

// The additional data of a sealed value is "<table>.<column>:<row key>",
// so a value copied into another column or another row does not open.
// Each row key is known when the value is written and survives a
// repository rename or transfer. Every read and write of a column builds
// its additional data through the one function here.

func buildSecretAAD(repoID int64, name string) string {
	return fmt.Sprintf("build_secrets.value:%d/%s", repoID, name)
}

func webhookAAD(id int64) string { return fmt.Sprintf("webhooks.secret:%d", id) }

func mirrorAAD(id int64) string { return fmt.Sprintf("mirrors.token:%d", id) }

// pushTokenAAD names the owner as well as the token, so a handover to
// another account reseals the token.
func pushTokenAAD(userID int64, hash string) string {
	return fmt.Sprintf("push_devices.token:%d/%s", userID, hash)
}

type secretColumn struct {
	table, column string
	// key selects the two parts of the row key, an integer and a text.
	key string
	aad func(n int64, s string) string
}

// secretColumns are the columns sealed under the key file (#273).
var secretColumns = []secretColumn{
	{"build_secrets", "value", "repo_id, name", buildSecretAAD},
	{"webhooks", "secret", "id, ''", func(id int64, _ string) string { return webhookAAD(id) }},
	{"mirrors", "token", "id, ''", func(id int64, _ string) string { return mirrorAAD(id) }},
	{"push_devices", "token", "user_id, COALESCE(token_hash, '')", pushTokenAAD},
}

// SetKeyring sets the keys the secret columns are sealed under.
func (s *Store) SetKeyring(k *seal.Keyring) { s.secrets = k }

// Keyring is the loaded key file, nil when none is set.
func (s *Store) Keyring() *seal.Keyring { return s.secrets }

// sealValue seals v for storage. An empty value stays empty: for
// webhooks and mirrors it means there is no secret.
func (s *Store) sealValue(aad, v string) (string, error) {
	if s.secrets == nil || v == "" {
		return v, nil
	}
	return s.secrets.Seal(aad, v)
}

// openValue returns a stored value in clear. A value not yet sealed is
// returned as stored: rows from before sealing existed stay readable
// until ResealSecrets reaches them.
func (s *Store) openValue(aad, v string) (string, error) {
	if !seal.IsSealed(v) {
		return v, nil
	}
	if s.secrets == nil {
		return "", errors.New("value is sealed and no secret key is loaded")
	}
	return s.secrets.Open(aad, v)
}

// tokenHash is the lookup key for a push device token.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type secretRow struct {
	rowid int64
	value string
	aad   string
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func secretRows(q queryer, c secretColumn) ([]secretRow, error) {
	rows, err := q.Query(fmt.Sprintf("SELECT rowid, %s, %s FROM %s WHERE %s != ''", c.column, c.key, c.table, c.column))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretRow
	for rows.Next() {
		var r secretRow
		var n int64
		var k string
		if err := rows.Scan(&r.rowid, &r.value, &n, &k); err != nil {
			return nil, err
		}
		r.aad = c.aad(n, k)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResealSecrets fills push_devices.token_hash where it is missing, then
// seals every clear value in the secret columns and reseals every value
// not under the key file's current key. It runs in one write
// transaction: every store write of a secret seals inside its own
// transaction, so a write either lands before this one and is resealed,
// or after it and is sealed under the key this one saw. It returns how
// many values it rewrote.
func (s *Store) ResealSecrets() (int, error) {
	if s.secrets == nil {
		return 0, errors.New("no secret key loaded")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cur, err := s.secrets.CurrentID()
	if err != nil {
		return 0, err
	}

	// A token without a hash was written before sealing, so it is clear.
	rows, err := tx.Query("SELECT id, token FROM push_devices WHERE token_hash IS NULL")
	if err != nil {
		return 0, err
	}
	var missing []secretRow
	for rows.Next() {
		var r secretRow
		if err := rows.Scan(&r.rowid, &r.value); err != nil {
			rows.Close()
			return 0, err
		}
		missing = append(missing, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range missing {
		if seal.IsSealed(r.value) {
			return 0, fmt.Errorf("push_devices row %d: sealed token without a token_hash", r.rowid)
		}
		if _, err := tx.Exec("UPDATE push_devices SET token_hash = ? WHERE id = ?", tokenHash(r.value), r.rowid); err != nil {
			return 0, err
		}
	}

	n := 0
	for _, c := range secretColumns {
		rows, err := secretRows(tx, c)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			if id, ok := seal.KeyID(r.value); ok && id == cur {
				continue
			}
			plain, err := s.openValue(r.aad, r.value)
			if err != nil {
				return 0, fmt.Errorf("%s.%s row %d: %w", c.table, c.column, r.rowid, err)
			}
			sealed, err := s.secrets.Seal(r.aad, plain)
			if err != nil {
				return 0, err
			}
			if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE rowid = ?", c.table, c.column), sealed, r.rowid); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, tx.Commit()
}

// SecretColumnUse is one secret column's values by the id of the key
// that sealed them ("" for a value still in clear), and the values that
// do not open under the loaded key file.
type SecretColumnUse struct {
	Column string // "<table>.<column>"
	ByKey  map[string]int
	Failed []SecretFailure
}

// SecretFailure is a stored value that does not open.
type SecretFailure struct {
	RowID int64
	Err   error
}

// SecretReport opens every value in the secret columns and counts them
// per column by key id. A value that does not open is listed rather than
// ending the scan.
func (s *Store) SecretReport() ([]SecretColumnUse, error) {
	var out []SecretColumnUse
	for _, c := range secretColumns {
		rows, err := secretRows(s.DB, c)
		if err != nil {
			return nil, err
		}
		u := SecretColumnUse{Column: c.table + "." + c.column, ByKey: map[string]int{}}
		for _, r := range rows {
			if _, err := s.openValue(r.aad, r.value); err != nil {
				u.Failed = append(u.Failed, SecretFailure{RowID: r.rowid, Err: err})
				continue
			}
			id, _ := seal.KeyID(r.value)
			u.ByKey[id]++
		}
		out = append(out, u)
	}
	return out, nil
}

// SecretKeyUse counts the values in the secret columns by the id of the
// key that sealed them ("" for a value still in clear), opening each
// one, so a wrong or incomplete key file is an error naming the row.
func (s *Store) SecretKeyUse() (map[string]int, error) {
	report, err := s.SecretReport()
	if err != nil {
		return nil, err
	}
	use := map[string]int{}
	for _, u := range report {
		if len(u.Failed) > 0 {
			f := u.Failed[0]
			return nil, fmt.Errorf("%s row %d: %w", u.Column, f.RowID, f.Err)
		}
		for id, n := range u.ByKey {
			use[id] += n
		}
	}
	return use, nil
}
