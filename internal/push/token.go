// Package push delivers activity notices to Apple devices over APNs: the
// third delivery route beside the inbox row and the activity mail, with
// the bounded-retry discipline the mail queue and webhook deliverer use.
package push

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// tokenLifetime is how long a provider token is reused. APNs accepts one
// for an hour and answers TooManyProviderTokenUpdates if they are minted
// faster than roughly once every twenty minutes, so the useful window is
// between the two.
const tokenLifetime = 50 * time.Minute

type tokenSource struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	now    func() time.Time

	mu     sync.Mutex
	cached string
	issued time.Time
}

func newTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *tokenSource {
	return &tokenSource{key: key, keyID: keyID, teamID: teamID, now: time.Now}
}

// token returns the cached provider token, minting a new one when the old
// one is near its end.
func (t *tokenSource) token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.cached != "" && now.Sub(t.issued) < tokenLifetime {
		return t.cached, nil
	}
	tok, err := t.sign(now)
	if err != nil {
		return "", err
	}
	t.cached, t.issued = tok, now
	return tok, nil
}

func (t *tokenSource) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": t.keyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": t.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, t.key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS wants the raw pair, each left-padded to the curve's byte size —
	// not ecdsa.SignASN1's DER. A DER signature is well-formed ECDSA and
	// is rejected by every JWT verifier, APNs included.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}
