package sshd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
		code := Exec(cfg, st, nil, nil, bob, key, control.Term{}, "git-receive-pack alice/app",
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
		code := Exec(cfg, st, packs, nil, bob, key, control.Term{}, service+" alice/app",
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
	if code := Exec(cfg, st, packs, nil, alice, key, control.Term{}, "git-upload-pack alice/app",
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
	if code := Exec(cfg, st, packs, nil, alice, key, control.Term{}, "git-receive-pack alice/app",
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
		codec <- Exec(cfg, st, packs, nil, alice, key, control.Term{}, "git-upload-pack alice/app",
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

// A client that stops reading is cut after packlimit.StallDeadline.
func TestCloneKilledWhenClientStopsReading(t *testing.T) {
	old := packlimit.StallDeadline
	packlimit.StallDeadline = 200 * time.Millisecond
	t.Cleanup(func() { packlimit.StallDeadline = old })
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
	if code := Exec(cfg, st, packs, nil, alice, key, control.Term{}, "git-upload-pack alice/app",
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
	code := Exec(cfg, st, packs, nil, bob, key, control.Term{}, "git-upload-pack alice/secret",
		strings.NewReader(""), &out, &errOut, nil, nil, nil)
	if code != protocol.ExitNotFound || strings.Contains(errOut.String(), "busy") {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}

// hungUpStdin is a client that sends nothing until hangUp, which ends
// its stdin the way a closed channel does.
func hungUpStdin(t *testing.T) (r *os.File, hangUp func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, func() { w.Close() }
}

// queuedFor waits until principal has a push waiting on l: a probe that
// gives up at once is then refused busy rather than queued.
func queuedFor(t *testing.T, l *packlimit.Limiter, principal string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := l.Acquire(closed(), principal); errors.Is(err, packlimit.ErrBusy) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no push queued for %s", principal)
}

// With push_per_principal 1 one account runs one push, queues a second
// and is refused a third, while another principal still gets in. A
// client hanging up frees its slot for the one queued behind it.
func TestPushPerPrincipalCap(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(2, 1, 16, 10*time.Second)
	push := func(k store.SSHKey, stdin io.Reader, errOut io.Writer) <-chan int {
		codec := make(chan int, 1)
		go func() {
			codec <- Exec(cfg, st, nil, pushes, alice, k, control.Term{}, "git-receive-pack alice/app",
				stdin, io.Discard, errOut, nil, nil, nil)
		}()
		return codec
	}
	exited := func(codec <-chan int, want int, what string) {
		t.Helper()
		select {
		case code := <-codec:
			if code != want {
				t.Fatalf("%s: exit %d", what, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s still running", what)
		}
	}
	principal := "user:" + strconv.FormatInt(alice.ID, 10)

	in1, hangUp1 := hungUpStdin(t)
	first := push(key, in1, io.Discard)
	// The first holds alice's one slot once a probe cannot take it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := pushes.Acquire(closed(), principal)
		if err != nil {
			break
		}
		r()
		if time.Now().After(deadline) {
			t.Fatal("first push never took a slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
	in2, hangUp2 := hungUpStdin(t)
	second := push(key, in2, io.Discard)
	queuedFor(t, pushes, principal)

	var errOut bytes.Buffer
	if code := Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
		strings.NewReader(""), io.Discard, &errOut, nil, nil, nil); code != protocol.ExitFailure ||
		!strings.Contains(errOut.String(), "limit of concurrent pushes") {
		t.Fatalf("third push: exit %d: %q", code, errOut.String())
	}

	// A deploy key on the same account is its own principal: it takes
	// the second global slot while alice's push waits.
	deploy := store.SSHKey{ID: 2, Scope: "deploy:" + strconv.FormatInt(repo.ID, 10) + ":rw", Fingerprint: "SHA256:deploy"}
	exited(push(deploy, strings.NewReader("0000"), io.Discard), protocol.ExitOK, "deploy key push")

	// receive-pack fails a client that hangs up before sending anything.
	hangUp1()
	exited(first, protocol.ExitFailure, "first push")
	hangUp2()
	exited(second, protocol.ExitFailure, "second push")
	for _, p := range []string{"a", "b"} {
		r, err := pushes.Acquire(nil, p)
		if err != nil {
			t.Fatalf("slot not released: %v", err)
		}
		defer r()
	}
}

// A revoked key kills a push waiting on its client, and the slot it
// held comes back.
func TestPushKilledWhenKeyRevokedReleasesSlot(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(1, 1, 0, time.Second)
	revoked := make(chan struct{})
	codec := make(chan int, 1)
	go func() {
		codec <- Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
			silentStdin(t), io.Discard, io.Discard, nil, nil, revoked)
	}()
	slotTaken(t, pushes)
	close(revoked)
	pushEnded(t, codec, pushes, 5*time.Second)
}

// slotTaken waits until l's one slot is held.
func slotTaken(t *testing.T, l *packlimit.Limiter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := l.Acquire(closed(), "probe")
		if err != nil {
			return
		}
		r()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the push never took the slot")
}

// pushEnded requires a push to end, killed, within within, with l's
// slot free again.
func pushEnded(t *testing.T, codec <-chan int, l *packlimit.Limiter, within time.Duration) {
	t.Helper()
	select {
	case code := <-codec:
		if code != protocol.ExitFailure {
			t.Fatalf("exit %d, want the push killed", code)
		}
	case <-time.After(within):
		t.Fatal("push still running")
	}
	r, err := l.Acquire(nil, "elsewhere")
	if err != nil {
		t.Fatalf("slot not released after the kill: %v", err)
	}
	r()
}

// A push the key may not make is refused before it reaches the limiter.
func TestRefusedPushStaysOffLimiter(t *testing.T) {
	cfg, st, bob := execFixture(t)
	pushes := packlimit.New(1, 0, 0, time.Second)
	hold, err := pushes.Acquire(nil, "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	key := store.SSHKey{Scope: "full", Fingerprint: "SHA256:test"}
	var errOut bytes.Buffer
	code := Exec(cfg, st, nil, pushes, bob, key, control.Term{}, "git-receive-pack alice/app",
		strings.NewReader(""), io.Discard, &errOut, nil, nil, nil)
	if code != protocol.ExitDenied || strings.Contains(errOut.String(), "busy") {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}
