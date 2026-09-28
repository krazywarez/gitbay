package store

import (
	"slices"
	"testing"
	"time"
)

func tokenID(t *testing.T, s *Store, hash string) int64 {
	t.Helper()
	_, tok, err := s.APITokenUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	return tok.ID
}

// parent made child, child made grandchild and a key; the key belongs
// to another account, as admin user create --key makes one.
func tokenChain(t *testing.T) (*Store, int64, *[]Revoked) {
	t.Helper()
	s, uid, got := revokeFixture(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(uid, "parent", "h-parent", "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(uid, "child", "h-child", "full", nil, tokenID(t, s, "h-parent")); err != nil {
		t.Fatal(err)
	}
	child := tokenID(t, s, "h-child")
	if err := s.CreateAPIToken(uid, "grandchild", "h-grand", "read", nil, child); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKeyFrom(bob, "SHA256:k", "ssh-ed25519", []byte("k"), "full", "", KeyOrigin{CreatedByToken: child}); err != nil {
		t.Fatal(err)
	}
	return s, uid, got
}

func TestTokenRecordsItsCreator(t *testing.T) {
	s, uid, _ := tokenChain(t)
	toks, err := s.ListAPITokens(uid)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, tk := range toks {
		by[tk.Name] = tk.CreatedBy
	}
	if by["parent"] != "" || by["child"] != "parent" || by["grandchild"] != "child" {
		t.Fatalf("created by: %v", by)
	}
	bob, _ := s.UserByUsername("bob")
	keys, err := s.ListSSHKeys(bob.ID)
	if err != nil || len(keys) != 1 || keys[0].CreatedBy != "child" {
		t.Fatalf("key created by: %+v %v", keys, err)
	}
}

func TestRevokeAPITokenListsWhatItCreated(t *testing.T) {
	s, uid, got := tokenChain(t)
	c, err := s.RevokeAPIToken(uid, "parent", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Tokens, []string{"child", "grandchild"}) || !slices.Equal(c.Keys, []string{"SHA256:k"}) {
		t.Fatalf("created = %+v", c)
	}
	// Listed, not removed; the link to the revoked parent is gone.
	toks, _ := s.ListAPITokens(uid)
	if len(toks) != 2 || toks[0].Name != "child" || toks[0].CreatedBy != "" {
		t.Fatalf("tokens after revoke: %+v", toks)
	}
	if _, err := s.SSHKeyByFingerprint("SHA256:k"); err != nil {
		t.Fatalf("the key went: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("announced %+v with nothing revoked but the token", *got)
	}
}

func TestRevokeAPITokenWithCreated(t *testing.T) {
	s, uid, got := tokenChain(t)
	k, _ := s.SSHKeyByFingerprint("SHA256:k")
	if _, err := s.RevokeAPIToken(uid, "parent", true); err != nil {
		t.Fatal(err)
	}
	if toks, _ := s.ListAPITokens(uid); len(toks) != 0 {
		t.Fatalf("tokens left: %+v", toks)
	}
	if _, err := s.SSHKeyByFingerprint("SHA256:k"); err != ErrNotFound {
		t.Fatalf("key left: %v", err)
	}
	if len(*got) != 1 || !slices.Equal((*got)[0].KeyIDs, []int64{k.ID}) {
		t.Fatalf("announced %+v", *got)
	}
	if _, err := s.RevokeAPIToken(uid, "parent", true); err != ErrNotFound {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestAPITokenUserCarriesExpiry(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	exp := time.Now().Add(time.Hour)
	if err := s.CreateAPIToken(uid, "brief", "h-brief", "full", &exp, 0); err != nil {
		t.Fatal(err)
	}
	_, tok, err := s.APITokenUser("h-brief")
	if err != nil || tok.ExpiresAt == nil || tok.Name != "brief" || tok.ID == 0 {
		t.Fatalf("token %+v %v", tok, err)
	}
}
