package sshd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/packlimit"
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
		code := Exec(cfg, st, nil, bob, key, control.Term{}, "git-receive-pack alice/app",
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

func TestCloneRefusedWhenPackSlotsAreFull(t *testing.T) {
	cfg, st, bob := execFixture(t)
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	for _, service := range []string{"git-upload-pack", "git-upload-archive"} {
		var out, errOut bytes.Buffer
		code := Exec(cfg, st, packs, bob, key, control.Term{}, service+" alice/app",
			strings.NewReader(""), &out, &errOut, nil, nil, nil)
		if code != protocol.ExitFailure || !strings.Contains(errOut.String(), "busy") {
			t.Fatalf("%s: exit %d: %q", service, code, errOut.String())
		}
	}
}

// A push takes no pack slot: it runs while every slot is held. A clone
// gives its slot back once git has exited.
func TestPushBypassesPackLimitAndCloneReleasesSlot(t *testing.T) {
	cfg, st, _ := execFixture(t)
	alice, err := st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := gitutil.InitBare(control.RepoDir(cfg.Server.Root, "alice", "app"), "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	packs := packlimit.New(1, 0, 0, time.Second)

	var out, errOut bytes.Buffer
	if code := Exec(cfg, st, packs, alice, key, control.Term{}, "git-upload-pack alice/app",
		strings.NewReader("0000"), &out, &errOut, nil, nil, nil); code != protocol.ExitOK {
		t.Fatalf("clone: exit %d: %s", code, errOut.String())
	}
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatalf("slot not released after the clone: %v", err)
	}
	defer hold()

	out.Reset()
	errOut.Reset()
	if code := Exec(cfg, st, packs, alice, key, control.Term{}, "git-receive-pack alice/app",
		strings.NewReader("0000"), &out, &errOut, nil, nil, nil); code != protocol.ExitOK {
		t.Fatalf("push with slots full: exit %d: %s", code, errOut.String())
	}
}

// cloneFixture adds an empty bare alice/app on disk and returns alice.
func cloneFixture(t *testing.T) (config.Config, *store.Store, store.User) {
	t.Helper()
	cfg, st, _ := execFixture(t)
	alice, err := st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := gitutil.InitBare(control.RepoDir(cfg.Server.Root, "alice", "app"), "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return cfg, st, alice
}

// silentStdin is a client that sends nothing and never hangs up. It is
// an *os.File, so git reads it directly: git exits only when killed.
func silentStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r
}

// killedClone runs a clone of alice/app with the given channels and
// requires it to end, killed, within five seconds, with its slot free.
func killedClone(t *testing.T, stdout io.Writer, done, stopping, revoked <-chan struct{}) {
	t.Helper()
	cfg, st, alice := cloneFixture(t)
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	packs := packlimit.New(1, 0, 0, time.Second)
	codec := make(chan int, 1)
	go func() {
		codec <- Exec(cfg, st, packs, alice, key, control.Term{}, "git-upload-pack alice/app",
			silentStdin(t), stdout, io.Discard, done, stopping, revoked)
	}()
	select {
	case code := <-codec:
		if code != protocol.ExitFailure {
			t.Fatalf("exit %d, want the clone killed", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clone still running")
	}
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatalf("slot not released after the kill: %v", err)
	}
	hold()
}

func closed() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func TestCloneKilledWhenClientLeaves(t *testing.T) {
	killedClone(t, io.Discard, closed(), nil, nil)
}

// A revoked key ends a clone even during a restart.
func TestCloneKilledWhenKeyRevoked(t *testing.T) {
	killedClone(t, io.Discard, nil, closed(), closed())
}

// A client that stops reading is cut after stallDeadline.
func TestCloneKilledWhenClientStopsReading(t *testing.T) {
	old := stallDeadline
	stallDeadline = 200 * time.Millisecond
	t.Cleanup(func() { stallDeadline = old })
	r, w := io.Pipe()
	t.Cleanup(func() { r.Close() })
	killedClone(t, w, nil, nil, nil)
}

// On a restart (done and stopping both closed) a running clone finishes.
func TestCloneRunsOnDuringRestart(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	packs := packlimit.New(1, 0, 0, time.Second)
	var errOut bytes.Buffer
	if code := Exec(cfg, st, packs, alice, key, control.Term{}, "git-upload-pack alice/app",
		strings.NewReader("0000"), io.Discard, &errOut, closed(), closed(), nil); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

// A request the key may not make is refused before it reaches the
// limiter: not found, never busy.
func TestRefusedCloneStaysOffLimiter(t *testing.T) {
	cfg, st, bob := execFixture(t)
	alice, err := st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", alice.ID, "secret", "private"); err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	var out, errOut bytes.Buffer
	code := Exec(cfg, st, packs, bob, key, control.Term{}, "git-upload-pack alice/secret",
		strings.NewReader(""), &out, &errOut, nil, nil, nil)
	if code != protocol.ExitNotFound || strings.Contains(errOut.String(), "busy") {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}
