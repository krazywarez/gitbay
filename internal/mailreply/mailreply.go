// Package mailreply mints and verifies the token in a notification's
// Reply-To address, reply+<token>@<domain> (#295). The token names the
// recipient, the repository and the thread, and an expiry; an HMAC under
// a key derived from the instance's secret key file binds them, so no
// row is stored per message.
package mailreply

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Lifetime is how long a notification's reply address is accepted.
const Lifetime = 30 * 24 * time.Hour

// Purpose is what the MAC key is derived for (seal.Keyring.Derive).
const Purpose = "gitbay mail reply token v1"

const (
	version = 1
	macLen  = 12
)

// Target is the thread a reply posts to, and who it posts as.
type Target struct {
	UserID int64
	RepoID int64
	Kind   string // "issue" or "mr"
	Number int64
}

var (
	ErrMalformed = errors.New("malformed reply token")
	ErrBadMAC    = errors.New("reply token does not verify")
	ErrExpired   = errors.New("reply token expired")
)

// Mail systems may fold the local part to lower case, so the token is
// lower-case base32.
var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// Mint returns the token for t, valid until expires, authenticated under
// keys[0].
func Mint(keys [][]byte, t Target, expires time.Time) (string, error) {
	if len(keys) == 0 {
		return "", errors.New("no key to mint a reply token under")
	}
	var kind byte
	switch t.Kind {
	case "issue":
		kind = 'i'
	case "mr":
		kind = 'm'
	default:
		return "", fmt.Errorf("reply token: unknown kind %q", t.Kind)
	}
	if t.UserID <= 0 || t.RepoID <= 0 || t.Number <= 0 {
		return "", errors.New("reply token: ids must be positive")
	}
	p := []byte{version, kind}
	p = binary.AppendUvarint(p, uint64(t.UserID))
	p = binary.AppendUvarint(p, uint64(t.RepoID))
	p = binary.AppendUvarint(p, uint64(t.Number))
	p = binary.AppendUvarint(p, uint64(expires.Unix()/3600))
	p = append(p, mac(keys[0], p)...)
	return strings.ToLower(enc.EncodeToString(p)), nil
}

// Verify checks token against every key and returns its target. An
// expired token that verifies returns its target with ErrExpired, so the
// refusal can name the account.
func Verify(keys [][]byte, token string, now time.Time) (Target, error) {
	up := strings.ToUpper(token)
	raw, err := enc.DecodeString(up)
	// Only the canonical encoding is accepted: base32 leaves spare bits
	// in the last character, and a character past the last byte.
	if err != nil || len(raw) < 2+4+macLen || enc.EncodeToString(raw) != up {
		return Target{}, ErrMalformed
	}
	p, sum := raw[:len(raw)-macLen], raw[len(raw)-macLen:]
	ok := false
	for _, k := range keys {
		if hmac.Equal(mac(k, p), sum) {
			ok = true
		}
	}
	if !ok {
		return Target{}, ErrBadMAC
	}
	if p[0] != version {
		return Target{}, ErrMalformed
	}
	var t Target
	switch p[1] {
	case 'i':
		t.Kind = "issue"
	case 'm':
		t.Kind = "mr"
	default:
		return Target{}, ErrMalformed
	}
	rest := p[2:]
	var v [4]uint64
	for i := range v {
		n, w := binary.Uvarint(rest)
		if w <= 0 || n > 1<<62 {
			return Target{}, ErrMalformed
		}
		v[i], rest = n, rest[w:]
	}
	if len(rest) != 0 {
		return Target{}, ErrMalformed
	}
	t.UserID, t.RepoID, t.Number = int64(v[0]), int64(v[1]), int64(v[2])
	if !now.Before(time.Unix(int64(v[3])*3600, 0)) {
		return t, ErrExpired
	}
	return t, nil
}

func mac(key, p []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(p)
	return m.Sum(nil)[:macLen]
}

// Address puts token into base, the configured reply address:
// reply@example.org becomes reply+<token>@example.org.
func Address(base, token string) string {
	local, domain, _ := strings.Cut(base, "@")
	return local + "+" + token + "@" + domain
}

// TokenFrom returns the token in addr when addr is base with a token
// added; the comparison ignores case.
func TokenFrom(base, addr string) (string, bool) {
	local, domain, ok := strings.Cut(base, "@")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || !strings.EqualFold(addr[i+1:], domain) {
		return "", false
	}
	tok, ok := cutPrefixFold(addr[:i], local+"+")
	if !ok || tok == "" {
		return "", false
	}
	return tok, true
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}
