package sshd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// execFixture: alice owns the public alice/app; bob has no grant on it.
func execFixture(t *testing.T) (config.Config, *store.Store, store.User) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", alice, "app", "public"); err != nil {
		t.Fatal(err)
	}
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.UserByID(bobID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.Root = t.TempDir()
	return cfg, st, bob
}

// A refused push leaves one row holding the target, the key and the exit
// code, whether runGit refused it or the account is not yet active.
func TestRefusedPushIsAudited(t *testing.T) {
	for _, pending := range []bool{false, true} {
		cfg, st, bob := execFixture(t)
		bob.Pending = pending
		key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
		var out, errOut bytes.Buffer
		code := Exec(cfg, st, bob, key, control.Term{}, "git-receive-pack alice/app",
			strings.NewReader(""), &out, &errOut, nil, nil, nil)
		if code != protocol.ExitDenied {
			t.Fatalf("pending %v: exit %d: %s", pending, code, errOut.String())
		}
		got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused git-receive-pack", Limit: 5})
		if err != nil || len(got) != 1 || got[0].Actor != "bob" {
			t.Fatalf("pending %v: entries %+v, %v", pending, got, err)
		}
		want := `{"argv":["alice/app"],"exit":4,"source":"SHA256:test"}`
		if got[0].Data != want {
			t.Fatalf("pending %v: data %s, want %s", pending, got[0].Data, want)
		}
	}
}
