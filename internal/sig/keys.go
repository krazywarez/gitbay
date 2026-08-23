package sig

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func keyIDHex(id uint64) string {
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = byte(id)
		id >>= 8
	}
	return hex.EncodeToString(b[:])
}

// PGPKeyMeta is what `pgp add` needs to persist about an imported key.
type PGPKeyMeta struct {
	Fingerprint string // primary key, lowercase hex
	Emails      []string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
}

// ParsePGPKey extracts registration metadata from an armored public key.
func ParsePGPKey(armored []byte) (PGPKeyMeta, error) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return PGPKeyMeta{}, fmt.Errorf("not a valid armored OpenPGP key: %w", err)
	}
	if len(ring) != 1 {
		return PGPKeyMeta{}, fmt.Errorf("expected exactly one key, got %d", len(ring))
	}
	e := ring[0]
	meta := PGPKeyMeta{
		Fingerprint: hex.EncodeToString(e.PrimaryKey.Fingerprint),
	}
	for _, id := range e.Identities {
		if id.UserId != nil && id.UserId.Email != "" {
			meta.Emails = append(meta.Emails, id.UserId.Email)
		}
	}
	// Primary key expiry from the self-signature.
	if selfSig, _ := e.PrimarySelfSignature(); selfSig != nil && selfSig.KeyLifetimeSecs != nil && *selfSig.KeyLifetimeSecs > 0 {
		t := e.PrimaryKey.CreationTime.Add(time.Duration(*selfSig.KeyLifetimeSecs) * time.Second)
		meta.ExpiresAt = &t
	}
	for _, rev := range e.Revocations {
		t := rev.CreationTime
		if meta.RevokedAt == nil || t.Before(*meta.RevokedAt) {
			meta.RevokedAt = &t
		}
	}
	return meta, nil
}
