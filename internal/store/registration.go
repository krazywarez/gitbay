package store

import (
	"errors"
	"time"
)

// CreateInvite stores an invite code hash bound to an email address.
func (s *Store) CreateInvite(codeHash, email string) error {
	_, err := s.DB.Exec("INSERT INTO invites (code_hash, email) VALUES (?, ?)", codeHash, email)
	return err
}

// ConsumeInvite redeems an invite exactly once, returning the address it was
// issued for. Used and unknown codes fail identically.
func (s *Store) ConsumeInvite(codeHash string) (string, error) {
	res, err := s.DB.Exec(
		"UPDATE invites SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE code_hash = ? AND used_at IS NULL",
		codeHash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	var email string
	err = s.DB.QueryRow("SELECT email FROM invites WHERE code_hash = ?", codeHash).Scan(&email)
	return email, err
}

// CreateEmailToken stores a verification code hash for one address.
func (s *Store) CreateEmailToken(userID int64, address, tokenHash string, ttl time.Duration) error {
	_, err := s.DB.Exec(
		"INSERT INTO email_tokens (token_hash, user_id, address, expires_at) VALUES (?, ?, ?, ?)",
		tokenHash, userID, address, fmtTime(time.Now().Add(ttl)))
	return err
}

// CountEmailTokensSince is how many verification codes an account has
// asked for since a moment, used or not.
func (s *Store) CountEmailTokensSince(userID int64, since time.Time) (int, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM email_tokens WHERE user_id = ? AND created_at > ?",
		userID, fmtTime(since)).Scan(&n)
	return n, err
}

// ConsumeEmailToken redeems a verification code for the given user.
func (s *Store) ConsumeEmailToken(userID int64, tokenHash string) (string, error) {
	res, err := s.DB.Exec(`
		UPDATE email_tokens SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE token_hash = ? AND user_id = ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, userID, fmtTime(time.Now()))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	var address string
	err = s.DB.QueryRow("SELECT address FROM email_tokens WHERE token_hash = ?", tokenHash).Scan(&address)
	return address, err
}

// EmailTokenBelongsToAnotherUser reports whether a live code exists but
// is owned by someone else. It answers only yes or no: naming the owner
// would turn a guessed code into an account oracle.
func (s *Store) EmailTokenBelongsToAnotherUser(userID int64, tokenHash string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM email_tokens
		WHERE token_hash = ? AND user_id != ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, userID, fmtTime(time.Now())).Scan(&n)
	return n > 0, err
}

// CreateRegisteredUser makes a self-registered account, pending until its
// email is verified.
func (s *Store) CreateRegisteredUser(username string, pending bool) (int64, error) {
	if taken, err := ownerNameTaken(s.DB, username); err != nil {
		return 0, err
	} else if taken {
		return 0, errors.New("that username is taken")
	}
	res, err := s.DB.Exec("INSERT INTO users (username, pending) VALUES (?, ?)", username, boolInt(pending))
	if err != nil {
		if isUniqueErr(err) {
			return 0, errors.New("that username is taken")
		}
		return 0, err
	}
	return res.LastInsertId()
}

// ClearPending activates a pending account.
func (s *Store) ClearPending(userID int64) error {
	_, err := s.DB.Exec("UPDATE users SET pending = 0 WHERE id = ?", userID)
	return err
}

// EmailInUse reports whether an address is attached to any account.
func (s *Store) EmailInUse(address string) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM emails WHERE address = ?", address).Scan(&n)
	return n > 0, err
}

// RedeemInvite performs the whole invite registration in one transaction:
// consume the code, create the user, attach the invite's email as verified,
// register the key. Any failure rolls everything back — the invite stays
// redeemable and no partial account exists.
func (s *Store) RedeemInvite(codeHash, username, keyFP, keyAlgo string, keyBlob []byte) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"UPDATE invites SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE code_hash = ? AND used_at IS NULL",
		codeHash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	var email string
	if err := tx.QueryRow("SELECT email FROM invites WHERE code_hash = ?", codeHash).Scan(&email); err != nil {
		return "", err
	}

	if taken, err := ownerNameTaken(tx, username); err != nil {
		return "", err
	} else if taken {
		return "", errors.New("that username is taken")
	}
	ures, err := tx.Exec("INSERT INTO users (username) VALUES (?)", username)
	if err != nil {
		return "", err
	}
	uid, err := ures.LastInsertId()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO emails (user_id, address, verified_at, verified_by, is_primary)
		 VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'smtp', 1)`, uid, email); err != nil {
		if isUniqueErr(err) {
			return "", errors.New("the invited address already belongs to an account")
		}
		return "", err
	}
	if _, err := tx.Exec(
		"INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, ?, ?, ?, 'full')",
		uid, keyFP, keyAlgo, keyBlob); err != nil {
		if isUniqueErr(err) {
			return "", ErrDuplicateKey
		}
		return "", err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return "", err
	}
	return email, tx.Commit()
}

// RegisterOpen performs open registration in one transaction: pending user,
// unverified email, key. Failure leaves nothing behind.
func (s *Store) RegisterOpen(username, email, keyFP, keyAlgo string, keyBlob []byte) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if taken, err := ownerNameTaken(tx, username); err != nil {
		return 0, err
	} else if taken {
		return 0, errors.New("that username is taken")
	}
	ures, err := tx.Exec("INSERT INTO users (username, pending) VALUES (?, 1)", username)
	if err != nil {
		return 0, err
	}
	uid, err := ures.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		"INSERT INTO emails (user_id, address, is_primary) VALUES (?, ?, 1)", uid, email); err != nil {
		if isUniqueErr(err) {
			return 0, errors.New("that address already belongs to an account")
		}
		return 0, err
	}
	if _, err := tx.Exec(
		"INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, ?, ?, ?, 'full')",
		uid, keyFP, keyAlgo, keyBlob); err != nil {
		if isUniqueErr(err) {
			return 0, ErrDuplicateKey
		}
		return 0, err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return 0, err
	}
	return uid, tx.Commit()
}
