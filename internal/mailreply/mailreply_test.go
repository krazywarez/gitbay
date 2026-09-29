package mailreply

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	keyA = []byte("0123456789abcdef0123456789abcdef")
	keyB = []byte("fedcba9876543210fedcba9876543210")
	now  = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
)

func mint(t *testing.T, keys [][]byte, tg Target) string {
	t.Helper()
	tok, err := Mint(keys, tg, now.Add(Lifetime))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRoundTrip(t *testing.T) {
	for _, tg := range []Target{
		{UserID: 1, RepoID: 2, Kind: "issue", Number: 3},
		{UserID: 1 << 40, RepoID: 99999, Kind: "mr", Number: 123456},
	} {
		tok := mint(t, [][]byte{keyA}, tg)
		if tok != strings.ToLower(tok) {
			t.Errorf("token %q is not lower case", tok)
		}
		// The address's local part stays within 64 octets.
		if l := len("reply+" + tok); l > 64 {
			t.Errorf("local part is %d octets", l)
		}
		got, err := Verify([][]byte{keyA}, tok, now)
		tg.Expires = now.Add(Lifetime)
		if err != nil || got != tg {
			t.Errorf("Verify = %+v, %v; want %+v", got, err, tg)
		}
		if !got.Issued().Equal(now) {
			t.Errorf("Issued = %v, want %v", got.Issued(), now)
		}
		// A mail system that upper-cases the local part does not break it.
		if got, err := Verify([][]byte{keyA}, strings.ToUpper(tok), now); err != nil || got != tg {
			t.Errorf("upper-case Verify = %+v, %v", got, err)
		}
	}
}

// A token minted before a rotation verifies while the old key is in the file.
func TestRotation(t *testing.T) {
	tg := Target{UserID: 1, RepoID: 2, Kind: "issue", Number: 3}
	tok := mint(t, [][]byte{keyA}, tg)
	if _, err := Verify([][]byte{keyB, keyA}, tok, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify([][]byte{keyB}, tok, now); !errors.Is(err, ErrBadMAC) {
		t.Fatalf("retired key: %v", err)
	}
}

func TestTamper(t *testing.T) {
	tok := mint(t, [][]byte{keyA}, Target{UserID: 1, RepoID: 2, Kind: "issue", Number: 3})
	for i := range tok {
		c := byte('a')
		if tok[i] == 'a' {
			c = 'b'
		}
		bad := tok[:i] + string(c) + tok[i+1:]
		if _, err := Verify([][]byte{keyA}, bad, now); err == nil {
			t.Fatalf("changed character %d verified", i)
		}
	}
	for _, bad := range []string{"", "x", "!!!!", tok[:len(tok)-1], tok + "a"} {
		if _, err := Verify([][]byte{keyA}, bad, now); err == nil {
			t.Errorf("%q verified", bad)
		}
	}
}

func TestExpiry(t *testing.T) {
	tg := Target{UserID: 1, RepoID: 2, Kind: "mr", Number: 3}
	tok := mint(t, [][]byte{keyA}, tg)
	if _, err := Verify([][]byte{keyA}, tok, now.Add(Lifetime-time.Hour)); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	got, err := Verify([][]byte{keyA}, tok, now.Add(Lifetime+time.Hour))
	tg.Expires = now.Add(Lifetime)
	if !errors.Is(err, ErrExpired) || got != tg {
		t.Fatalf("after expiry: %+v, %v", got, err)
	}
}

// Two recipients of one notification get different tokens, and neither
// verifies as the other.
func TestCrossUser(t *testing.T) {
	a := mint(t, [][]byte{keyA}, Target{UserID: 1, RepoID: 2, Kind: "issue", Number: 3})
	b := mint(t, [][]byte{keyA}, Target{UserID: 4, RepoID: 2, Kind: "issue", Number: 3})
	if a == b {
		t.Fatal("two recipients share a token")
	}
	ga, _ := Verify([][]byte{keyA}, a, now)
	gb, _ := Verify([][]byte{keyA}, b, now)
	if ga.UserID != 1 || gb.UserID != 4 {
		t.Fatalf("got users %d and %d", ga.UserID, gb.UserID)
	}
}

func TestMintRefuses(t *testing.T) {
	for _, tg := range []Target{
		{UserID: 1, RepoID: 2, Kind: "build", Number: 3},
		{UserID: 0, RepoID: 2, Kind: "issue", Number: 3},
	} {
		if _, err := Mint([][]byte{keyA}, tg, now); err == nil {
			t.Errorf("minted %+v", tg)
		}
	}
	if _, err := Mint(nil, Target{UserID: 1, RepoID: 2, Kind: "issue", Number: 3}, now); err == nil {
		t.Error("minted with no key")
	}
}

func TestAddress(t *testing.T) {
	a := Address("reply@gitbay.example", "abc")
	if a != "reply+abc@gitbay.example" {
		t.Fatalf("Address = %q", a)
	}
	for addr, want := range map[string]string{
		"reply+abc@gitbay.example": "abc",
		"Reply+ABC@GITBAY.example": "ABC",
		"reply@gitbay.example":     "",
		"reply+@gitbay.example":    "",
		"reply+abc@other.example":  "",
		"other+abc@gitbay.example": "",
	} {
		got, ok := TokenFrom("reply@gitbay.example", addr)
		if got != want || ok != (want != "") {
			t.Errorf("TokenFrom(%q) = %q, %v", addr, got, ok)
		}
	}
}
