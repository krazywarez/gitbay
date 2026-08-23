package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/krazywarez/forge/internal/sig"
)

// AddPGPKey registers an OpenPGP key and bumps the key epoch.
func (s *Store) AddPGPKey(userID int64, fingerprint, armored, uidsJSON string, expiresAt, revokedAt *time.Time) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"INSERT INTO pgp_keys (user_id, fingerprint, armored, uids_json, expires_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?)",
		userID, fingerprint, armored, uidsJSON, timePtr(expiresAt), timePtr(revokedAt)); err != nil {
		if isUniqueErr(err) {
			return ErrDuplicateKey
		}
		return err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemovePGPKey(userID int64, fingerprint string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM pgp_keys WHERE user_id = ? AND fingerprint = ?", userID, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type PGPKey struct {
	Fingerprint string
	UIDsJSON    string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
}

func (s *Store) ListPGPKeys(userID int64) ([]PGPKey, error) {
	rows, err := s.DB.Query(
		"SELECT fingerprint, uids_json, expires_at, revoked_at FROM pgp_keys WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PGPKey
	for rows.Next() {
		var k PGPKey
		var exp, rev sql.NullString
		if err := rows.Scan(&k.Fingerprint, &k.UIDsJSON, &exp, &rev); err != nil {
			return nil, err
		}
		k.ExpiresAt = parseTime(exp)
		k.RevokedAt = parseTime(rev)
		out = append(out, k)
	}
	return out, rows.Err()
}

// VerifyEmail marks an address verified and bumps the key epoch (email
// verification is a trust input for signature states).
func (s *Store) VerifyEmail(userID int64, address, by string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`UPDATE emails SET verified_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), verified_by = ?
		 WHERE user_id = ? AND address = ?`, by, userID, address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SigDB adapts Store to the verifier's interface and owns the epoch cache.
type SigDB struct{ *Store }

func (d SigDB) PGPKeyByIssuer(keyIDHex string) (sig.PGPKeyInfo, string, bool, error) {
	var info sig.PGPKeyInfo
	var fpr string
	var exp, rev sql.NullString
	err := d.DB.QueryRow(
		"SELECT user_id, fingerprint, armored, expires_at, revoked_at FROM pgp_keys WHERE fingerprint LIKE '%' || ?",
		keyIDHex).Scan(&info.UserID, &fpr, &info.Armored, &exp, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return info, "", false, nil
	}
	if err != nil {
		return info, "", false, err
	}
	info.ExpiresAt = parseTime(exp)
	info.RevokedAt = parseTime(rev)
	return info, fpr, true, nil
}

func (d SigDB) SSHSignerByFingerprint(fp string) (sig.SSHKeyInfo, bool, error) {
	k, err := d.SSHKeyByFingerprint(fp)
	if errors.Is(err, ErrNotFound) {
		return sig.SSHKeyInfo{}, false, nil
	}
	if err != nil {
		return sig.SSHKeyInfo{}, false, err
	}
	return sig.SSHKeyInfo{UserID: k.UserID, Fingerprint: k.Fingerprint}, true, nil
}

func (d SigDB) VerifiedEmails(userID int64) ([]string, error) {
	rows, err := d.DB.Query(
		"SELECT address FROM emails WHERE user_id = ? AND verified_at IS NOT NULL", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CachedSignature returns a cached result and whether it is current at the
// given epoch.
func (s *Store) CachedSignature(repoID int64, sha string, epoch int64) (sig.Result, bool, error) {
	var r sig.Result
	var state string
	var signer sql.NullInt64
	var fpr sql.NullString
	var rowEpoch int64
	err := s.DB.QueryRow(
		"SELECT state, signer_user_id, key_fingerprint, key_epoch FROM commit_signatures WHERE repo_id = ? AND commit_sha = ?",
		repoID, sha).Scan(&state, &signer, &fpr, &rowEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if rowEpoch < epoch {
		return r, false, nil // stale: trust inputs changed since this was computed
	}
	r.State = sig.State(state)
	r.SignerUserID = signer.Int64
	r.KeyFingerprint = fpr.String
	return r, true, nil
}

func (s *Store) StoreSignature(repoID int64, sha string, r sig.Result, epoch int64) error {
	var signer any
	if r.SignerUserID != 0 {
		signer = r.SignerUserID
	}
	var fpr any
	if r.KeyFingerprint != "" {
		fpr = r.KeyFingerprint
	}
	_, err := s.DB.Exec(`
		INSERT INTO commit_signatures (repo_id, commit_sha, state, signer_user_id, key_fingerprint, key_epoch)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, commit_sha) DO UPDATE SET
			state = excluded.state, signer_user_id = excluded.signer_user_id,
			key_fingerprint = excluded.key_fingerprint, key_epoch = excluded.key_epoch,
			checked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		repoID, sha, string(r.State), signer, fpr, epoch)
	return err
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", s.String)
	if err != nil {
		return nil
	}
	return &t
}
