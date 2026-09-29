package lfs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestTokenCarriesTheKey(t *testing.T) {
	secret := []byte("secret")
	now := time.Now()
	tok := Sign(secret, 7, 42, "SHA256:k", "upload", now)
	g, ok := Verify(secret, tok, now)
	if !ok || g != (Grant{RepoID: 7, KeyID: 42, KeyPin: KeyPin("SHA256:k"), Op: "upload"}) {
		t.Fatalf("Verify = %+v, %v", g, ok)
	}
	if _, ok := Verify(secret, tok, now.Add(TokenTTL+time.Second)); ok {
		t.Error("an expired token verified")
	}
	if _, ok := Verify([]byte("other"), tok, now); ok {
		t.Error("a token verified under another secret")
	}
	if g, ok := Verify(secret, Sign(secret, 7, 0, "", "download", now), now); !ok || g.KeyID != 0 || g.KeyPin != "" {
		t.Errorf("anonymous grant = %+v, %v", g, ok)
	}
}

// A token minted before tokens named their key has three fields. It is
// refused, not read as a grant bound to no key (#285).
func TestUnboundTokenRefused(t *testing.T) {
	secret := []byte("secret")
	payload := fmt.Sprintf("%d:%s:%d", 7, "upload", time.Now().Add(TokenTTL).Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	tok := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if g, ok := Verify(secret, tok, time.Now()); ok {
		t.Fatalf("a pre-upgrade token verified: %+v", g)
	}
}

// A token minted before tokens carried the key's fingerprint has four
// fields. It is refused (#303).
func TestUnpinnedTokenRefused(t *testing.T) {
	secret := []byte("secret")
	payload := fmt.Sprintf("%d:%d:%s:%d", 7, 42, "upload", time.Now().Add(TokenTTL).Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	tok := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if g, ok := Verify(secret, tok, time.Now()); ok {
		t.Fatalf("an unpinned token verified: %+v", g)
	}
}
