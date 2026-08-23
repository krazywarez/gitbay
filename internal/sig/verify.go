package sig

import (
	"bytes"
	"slices"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

type State string

const (
	Verified            State = "verified"
	SignedUnknownKey    State = "signed_unknown_key"
	SignedEmailMismatch State = "signed_email_mismatch"
	SignedKeyExpired    State = "signed_key_expired"
	SignedKeyRevoked    State = "signed_key_revoked"
	BadSignature        State = "bad_signature"
	Unsigned            State = "unsigned"
)

type Result struct {
	State          State
	SignerUserID   int64 // 0 when unknown
	KeyFingerprint string
}

// PGPKeyInfo is a registered OpenPGP key as the verifier needs it.
type PGPKeyInfo struct {
	UserID    int64
	Armored   string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// SSHKeyInfo is a registered SSH key as the verifier needs it.
type SSHKeyInfo struct {
	UserID      int64
	Fingerprint string
}

// DB is the store surface the verifier depends on. Implemented by
// store.SigDB.
type DB interface {
	// PGPKeyByIssuer finds a registered key whose fingerprint ends with the
	// issuer key id (16 hex chars, lowercase).
	PGPKeyByIssuer(keyIDHex string) (PGPKeyInfo, string, bool, error) // info, fingerprint, found
	SSHSignerByFingerprint(fp string) (SSHKeyInfo, bool, error)
	VerifiedEmails(userID int64) ([]string, error)
}

// VerifyCommit classifies one parsed commit. The commit's author email
// drives the identity check, per the plan.
func VerifyCommit(db DB, c *Commit) (Result, error) {
	switch KindOf(c.Signature) {
	case SigNone:
		return Result{State: Unsigned}, nil
	case SigOpenPGP:
		return verifyOpenPGP(db, c)
	case SigSSH:
		return verifySSH(db, c)
	default:
		return Result{State: BadSignature}, nil
	}
}

func verifyOpenPGP(db DB, c *Commit) (Result, error) {
	issuer, sigTime, ok := openpgpIssuer(c.Signature)
	if !ok {
		return Result{State: BadSignature}, nil
	}
	key, fpr, found, err := db.PGPKeyByIssuer(issuer)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{State: SignedUnknownKey, KeyFingerprint: issuer}, nil
	}
	res := Result{SignerUserID: key.UserID, KeyFingerprint: fpr}

	// Key-state policy comes before cryptography: a revoked or expired key
	// invalidates the trust claim no matter what the signature says, and
	// hard revocations make the library's own verdict on such keys
	// unpredictable.
	now := time.Now()
	if key.RevokedAt != nil && key.RevokedAt.Before(now) {
		res.State = SignedKeyRevoked
		return res, nil
	}
	if key.ExpiresAt != nil && key.ExpiresAt.Before(now) {
		res.State = SignedKeyExpired
		return res, nil
	}

	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader([]byte(key.Armored)))
	if err != nil {
		return Result{}, err
	}
	// Verify the cryptography at the signature's own creation time: key
	// expiry is our policy decision (above), not the library's.
	cfg := &packet.Config{Time: func() time.Time { return sigTime }}
	signer, err := openpgp.CheckArmoredDetachedSignature(
		ring, bytes.NewReader(c.Payload), bytes.NewReader(c.Signature), cfg)
	if err != nil || signer == nil {
		res.State = BadSignature
		return res, nil
	}

	// Author email must appear in a UID on the signing key AND be a
	// verified address on the owning account.
	uidMatch := false
	for _, id := range signer.Identities {
		if id.UserId != nil && id.UserId.Email == c.AuthorEmail {
			uidMatch = true
			break
		}
	}
	verified, err := db.VerifiedEmails(key.UserID)
	if err != nil {
		return Result{}, err
	}
	if !uidMatch || !slices.Contains(verified, c.AuthorEmail) {
		res.State = SignedEmailMismatch
		return res, nil
	}
	res.State = Verified
	return res, nil
}

// openpgpIssuer extracts the issuer key id (lowercase hex) and creation time
// from an armored signature without verifying it.
func openpgpIssuer(armored []byte) (string, time.Time, bool) {
	block, err := decodeArmor(armored, "PGP SIGNATURE")
	if err != nil {
		return "", time.Time{}, false
	}
	p, err := packet.Read(bytes.NewReader(block))
	if err != nil {
		return "", time.Time{}, false
	}
	sig, ok := p.(*packet.Signature)
	if !ok || sig.IssuerKeyId == nil {
		return "", time.Time{}, false
	}
	return keyIDHex(*sig.IssuerKeyId), sig.CreationTime, true
}
