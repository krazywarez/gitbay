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

// testServer is an embedded server over a fresh store holding alice
// with one full-scope key, and a client connected with that key.
type testServer struct {
	srv    *Server
	st     *store.Store
	client *ssh.Client
	uid    int64
	keyID  int64
	fp     string
}

func newTestServer(t *testing.T) testServer {
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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	fp := ssh.FingerprintSHA256(pub)
	if err := st.AddSSHKey(uid, fp, pub.Type(), pub.Marshal(), "full", "test"); err != nil {
		t.Fatal(err)
	}
	key, err := st.SSHKeyByFingerprint(fp)
	if err != nil {
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
	return testServer{srv: srv, st: st, client: client, uid: uid, keyID: key.ID, fp: fp}
}

// withBuild gives alice the public repo alice/app and a queued build 1
// whose log has one line.
func withBuild(t *testing.T, ts testServer) {
	t.Helper()
	repoID, err := ts.st.CreateRepo("user", ts.uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ts.st.CreateBuild(repoID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.st.AppendBuildLog(id, []byte("queued\n")); err != nil {
		t.Fatal(err)
	}
}

// followServer starts an embedded server holding alice, her public repo
// alice/app and a queued build 1 whose log has one line, and returns it
// with a client connected as alice.
func followServer(t *testing.T) (*Server, *ssh.Client) {
	t.Helper()
	ts := newTestServer(t)
	withBuild(t, ts)
	return ts.srv, ts.client
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

// An unregistered key is told its own fingerprint and the real host, and
// offered both the web and the ssh path to register.
func TestUnregisteredKeyMessageNamesFingerprintAndHost(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.Root = root
	// The settings link keeps the site URL's scheme and port.
	cfg.Server.SiteURL = "http://forge.test:8080/"
	cfg.Registration.Mode = "open"
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

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

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

	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	var exit *ssh.ExitError
	if err := sess.Run("whoami"); !errors.As(err, &exit) || exit.ExitStatus() != 4 {
		t.Fatalf("whoami ended with %v, want exit 4", err)
	}

	fp := ssh.FingerprintSHA256(signer.PublicKey())
	for _, want := range []string{fp, "forge.test", "add it at http://forge.test:8080/settings#keys\n", "ssh git@forge.test register"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("message missing %q:\n%s", want, stderr.String())
		}
	}
}

// authMeta is the connection metadata authenticate reads: only the
// remote address.
type authMeta struct {
	ssh.ConnMetadata
	addr net.Addr
}

func (m authMeta) RemoteAddr() net.Addr { return m.addr }

func authKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// authServer is a Server holding what authenticate uses: a store with a
// runner account's key, the registration mode, and a limiter of three
// failures a minute.
func authServer(t *testing.T, mode string) (*Server, ssh.PublicKey) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("ci", false)
	if err != nil {
		t.Fatal(err)
	}
	runner := authKey(t)
	if err := st.AddSSHKey(uid, ssh.FingerprintSHA256(runner), runner.Type(), runner.Marshal(), "runner", ""); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Registration.Mode = mode
	return &Server{cfg: cfg, st: st, authLimiter: newRateLimiter(3, time.Minute)}, runner
}

// With registration closed an unknown key counts against its address.
// Below the limit a known key's success clears the count. At the limit
// authenticate refuses before it looks at the key, so the runner's own
// key from that address is refused too and its success never runs to
// clear anything, until the window passes. Another address is not
// affected. This is why a build must not share the runner's source
// address (#260).
func TestAuthLockoutHoldsAgainstTheRunnersKey(t *testing.T) {
	s, runner := authServer(t, "closed")
	stranger := authKey(t)
	failTimes := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := s.authenticate(fromLoopback, stranger); err == nil {
				t.Fatal("unknown key admitted with registration closed")
			}
		}
	}

	failTimes(2)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner below the limit: %v", err)
	}
	failTimes(2)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner after its success cleared the count: %v", err)
	}

	failTimes(3)
	for i := 0; i < 2; i++ {
		if _, err := s.authenticate(fromLoopback, runner); err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("attempt %d from a locked-out address: %v, want refused", i+1, err)
		}
	}
	if _, err := s.authenticate(fromPublic, runner); err != nil {
		t.Fatalf("another address was locked out too: %v", err)
	}

	s.authLimiter.seen["127.0.0.1"].start = time.Now().Add(-2 * time.Minute)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner after the window passed: %v", err)
	}
}

// With registration open or by invite, an unknown key is admitted to run
// register and never counts, so no number of unknown-key attempts locks
// the runner's address out. gitbay.org runs open registration (#260).
func TestAuthUnknownKeyCountsOnlyWhenClosed(t *testing.T) {
	for _, mode := range []string{"open", "invite"} {
		s, runner := authServer(t, mode)
		for i := 0; i < 10; i++ {
			p, err := s.authenticate(fromLoopback, authKey(t))
			if err != nil || p.Extensions["anon-key"] == "" {
				t.Fatalf("%s: unknown key %d: %v %+v", mode, i+1, err, p)
			}
		}
		if _, err := s.authenticate(fromLoopback, runner); err != nil {
			t.Fatalf("%s: runner refused after unknown keys: %v", mode, err)
		}
	}
}

var (
	fromLoopback = authMeta{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}}
	fromPublic   = authMeta{addr: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 40000}}
)
