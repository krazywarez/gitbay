package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTokenShapeAndSignature(t *testing.T) {
	key := testKey(t)
	ts := newTokenSource(key, "KEYID123", "TEAMID456")
	tok, err := ts.token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want three dot-separated parts, got %d", len(parts))
	}

	var hdr struct{ Alg, Kid string }
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("header: %v", err)
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEYID123" {
		t.Fatalf("header = %+v", hdr)
	}

	// APNs provider tokens carry iss (team id) and iat, and nothing else.
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims["iss"] != "TEAMID456" {
		t.Fatalf("iss = %v", claims["iss"])
	}
	if _, ok := claims["iat"]; !ok {
		t.Fatal("no iat")
	}
	if len(claims) != 2 {
		t.Fatalf("unexpected claims: %v", claims)
	}

	// The signature is raw r||s, 64 bytes — not the ASN.1 DER that
	// ecdsa.SignASN1 returns. Sending DER gets every push rejected.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (raw r||s)", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Fatal("signature does not verify")
	}
}

func TestTokenCachedThenReminted(t *testing.T) {
	ts := newTokenSource(testKey(t), "K", "T")
	base := time.Now()
	ts.now = func() time.Time { return base }

	first, _ := ts.token()
	second, _ := ts.token()
	if first != second {
		t.Fatal("token reminted inside the cache window; APNs answers TooManyProviderTokenUpdates")
	}

	// Valid for an hour, not to be reminted faster than every twenty
	// minutes: refresh at fifty.
	ts.now = func() time.Time { return base.Add(51 * time.Minute) }
	third, _ := ts.token()
	if third == first {
		t.Fatal("token not reminted after fifty minutes")
	}
}
