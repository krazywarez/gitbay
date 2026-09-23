package sshd

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// followServer starts an embedded server holding alice, her public repo
// alice/app and a queued build 1 whose log has one line, and returns it
// with a client connected as alice.
func followServer(t *testing.T) (*Server, *ssh.Client) {
	t.Helper()
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
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateBuild(repoID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBuildLog(id, []byte("queued\n")); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	if err := st.AddSSHKey(uid, ssh.FingerprintSHA256(pub), pub.Type(), pub.Marshal(), "full", "test"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.Root = root
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return srv, client
}

// startFollow runs build log --follow on a new session and returns once
// the stored line has arrived, so the follow is past its first read.
func startFollow(t *testing.T, client *ssh.Client, stderr *bytes.Buffer) *ssh.Session {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.Stderr = stderr
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Start("build log alice/app 1 --follow"); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(out).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "queued\n" {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stored log never arrived")
	}
	return sess
}

func activeSessions(s *Server) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int32
	for c := range s.conns {
		n += c.active.Load()
	}
	return n
}

// Closing the session channel ends a follow of a queued build while the
// connection stays up, as a Ctrl-C does over the CLI's shared connection.
// Nothing else would end it for ten minutes.
func TestFollowEndsWhenChannelCloses(t *testing.T) {
	srv, client := followServer(t)
	var stderr bytes.Buffer
	sess := startFollow(t, client, &stderr)
	if n := activeSessions(srv); n != 1 {
		t.Fatalf("%d sessions active while following, want 1", n)
	}
	sess.Close()

	deadline := time.Now().Add(5 * time.Second)
	for activeSessions(srv) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the follow outlived its channel")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := client.NewSession(); err != nil {
		t.Fatalf("the connection did not survive the channel: %v", err)
	}
}

// A command that fails on its own while the server is stopping says
// nothing about a restart: only a follow that Stop ended does.
func TestStopLeavesOtherFailuresAlone(t *testing.T) {
	srv, client := followServer(t)
	srv.Stop()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	var exit *ssh.ExitError
	if err := sess.Run("repo show nosuch/repo"); !errors.As(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("repo show of a missing repository: %v, want exit 3", err)
	}
	if strings.Contains(stderr.String(), "restarting") {
		t.Errorf("stderr %q", stderr.String())
	}
}

// Stop ends a follow with exit 1 and says why, without closing the
// connection.
func TestStopEndsFollow(t *testing.T) {
	srv, client := followServer(t)
	var stderr bytes.Buffer
	sess := startFollow(t, client, &stderr)
	srv.Stop()

	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	select {
	case err := <-waited:
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 1 {
			t.Fatalf("follow ended with %v, want exit 1", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not end the follow")
	}
	if !strings.Contains(stderr.String(), "gitbay is restarting") {
		t.Errorf("stderr %q", stderr.String())
	}
}
