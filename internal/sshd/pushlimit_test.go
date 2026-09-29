package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

// A client that connects and sends nothing is cut at
// push_receive_timeout: the idle rule has not started, since no pack
// has begun. Its slot comes back.
func TestSilentPushKilledAtReceiveTimeout(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	cfg.Limits.PushIdle = "200ms"
	cfg.Limits.PushReceiveTimeout = "1s"
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(1, 1, 0, time.Second)
	codec := make(chan int, 1)
	start := time.Now()
	go func() {
		codec <- Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
			silentStdin(t), io.Discard, io.Discard, nil, nil, nil)
	}()
	slotTaken(t, pushes)
	pushEnded(t, codec, pushes, 5*time.Second)
	if d := time.Since(start); d < time.Second {
		t.Fatalf("killed after %s, before push_receive_timeout", d)
	}
}

// pktLine frames s as one pkt-line.
func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

const zeroSHA = "0000000000000000000000000000000000000000"

// Once the pack has begun, a client that goes silent is cut at
// push_idle, long before push_receive_timeout.
func TestPushKilledAtIdleOncePackStarts(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	cfg.Limits.PushIdle = "300ms"
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(1, 1, 0, time.Second)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	io.WriteString(w, pktLine(zeroSHA+" "+strings.Repeat("1", 40)+" refs/heads/main\x00report-status\n")+"0000PA")
	codec := make(chan int, 1)
	start := time.Now()
	go func() {
		codec <- Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
			r, io.Discard, io.Discard, nil, nil, nil)
	}()
	slotTaken(t, pushes)
	// The signature split across two writes still arms the rule.
	time.Sleep(100 * time.Millisecond)
	io.WriteString(w, "CK")
	pushEnded(t, codec, pushes, 5*time.Second)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("killed after %s", d)
	}
}

// A client silent for longer than push_idle between its commands and
// its pack, as while pack-objects compresses a large first push, still
// completes.
func TestPushSilentBeforePackCompletes(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	cfg.Limits.PushIdle = "300ms"
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(1, 1, 0, time.Second)

	src := t.TempDir()
	git := func(stdin string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	git("", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("", "add", ".")
	git("", "commit", "-q", "-m", "one")
	sha := strings.TrimSpace(string(git("", "rev-parse", "HEAD")))
	pack := git(sha+"\n", "pack-objects", "--revs", "--stdout", "-q")

	pr, pw := io.Pipe()
	go func() {
		io.WriteString(pw, pktLine(zeroSHA+" "+sha+" refs/heads/main\x00report-status\n")+"0000")
		time.Sleep(time.Second)
		pw.Write(pack)
		pw.Close()
	}()
	var errOut strings.Builder
	if code := Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
		pr, io.Discard, &errOut, nil, nil, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	out, err := exec.Command("git", "-C", control.RepoDir(cfg.Server.Root, "alice", "app"), "rev-parse", "refs/heads/main").Output()
	if err != nil || strings.TrimSpace(string(out)) != sha {
		t.Fatalf("main is %q (%v), want %s", out, err, sha)
	}
}

// A client that trickles bytes stays clear of push_idle but is cut when
// pre-receive has not started push_receive_timeout after the slot.
func TestTricklingPushKilledAtReceiveTimeout(t *testing.T) {
	cfg, st, alice := cloneFixture(t)
	cfg.Limits.PushIdle = "300ms"
	cfg.Limits.PushReceiveTimeout = "1s"
	key := store.SSHKey{ID: 1, Scope: "full", Fingerprint: "SHA256:test"}
	pushes := packlimit.New(1, 1, 0, time.Second)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// One long pkt-line whose payload arrives a byte at a time.
		if _, err := io.WriteString(w, "fff0"); err != nil {
			return
		}
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := w.Write([]byte("a")); err != nil {
					return
				}
			}
		}
	}()
	codec := make(chan int, 1)
	start := time.Now()
	go func() {
		codec <- Exec(cfg, st, nil, pushes, alice, key, control.Term{}, "git-receive-pack alice/app",
			r, io.Discard, io.Discard, nil, nil, nil)
	}()
	slotTaken(t, pushes)
	pushEnded(t, codec, pushes, 5*time.Second)
	if d := time.Since(start); d < time.Second {
		t.Fatalf("killed after %s, before push_receive_timeout", d)
	}
}

// A real push whose post-receive outlasts push_idle completes: git's
// side-band keepalives count as bytes to the client.
func TestPushSurvivesLongPostReceive(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "post-receive"), []byte("#!/bin/sh\nsleep 4\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitutil.InitBare(control.RepoDir(root, "alice", "app"), "main", hooks); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddSSHKey(uid, ssh.FingerprintSHA256(sshPub), sshPub.Type(), sshPub.Marshal(), "full", "test"); err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.Root = root
	cfg.Limits.PushIdle = "2s"
	pushes := packlimit.New(1, 1, 0, time.Second)
	srv, err := New(cfg, st, nil, pushes)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	src := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.test",
			"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.test",
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -F /dev/null -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -p %d", keyFile, port))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "one")
	start := time.Now()
	git("push", "git@127.0.0.1:alice/app", "main")
	if d := time.Since(start); d < 4*time.Second {
		t.Fatalf("push took %s; the post-receive did not run", d)
	}
	out, err := exec.Command("git", "-C", control.RepoDir(root, "alice", "app"), "rev-parse", "refs/heads/main").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("main not pushed: %v %s", err, out)
	}
	r, err := pushes.Acquire(nil, "elsewhere")
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	r()
}
