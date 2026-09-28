package httpd

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

func busyServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
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
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hold)
	var cfg config.Config
	cfg.Server.Root = t.TempDir()
	return &Server{cfg: cfg, st: st, packs: packs, stopping: make(chan struct{})}
}

func post(s *Server, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/alice/app/git-upload-pack", strings.NewReader(body))
	r.SetPathValue("owner", "alice")
	r.SetPathValue("repo", "app")
	w := httptest.NewRecorder()
	s.uploadPack(w, r)
	return w
}

func TestUploadPackBusyIs503(t *testing.T) {
	w := post(busyServer(t), "0000")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

// A protocol v2 ref listing generates no pack and is never queued.
func TestLsRefsBypassesTheLimit(t *testing.T) {
	w := post(busyServer(t), "0014command=ls-refs\n0000")
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("ls-refs was held to the pack limit")
	}
}

// A request with a valid bearer token counts against the account, the
// key SSH uses; anything else against the client address.
func TestPackPrincipal(t *testing.T) {
	s := busyServer(t)
	alice, err := s.st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.CreateAPIToken(alice.ID, "t", store.HashToken("secret"), "read", nil, 0); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/alice/app/git-upload-pack", nil)
	r.RemoteAddr = "192.0.2.7:4000"
	if got := s.packPrincipal(r); got != "ip:192.0.2.7" {
		t.Fatalf("anonymous: %q", got)
	}
	r.Header.Set("Authorization", "Bearer wrong")
	if got := s.packPrincipal(r); got != "ip:192.0.2.7" {
		t.Fatalf("bad token: %q", got)
	}
	r6 := httptest.NewRequest("POST", "/alice/app/git-upload-pack", nil)
	r6.RemoteAddr = "[2001:db8:1:2:3:4:5:6]:4000"
	if got := s.packPrincipal(r6); got != "ip:2001:db8:1:2::/64" {
		t.Fatalf("anonymous IPv6: %q", got)
	}
	want := "user:" + strconv.FormatInt(alice.ID, 10)
	r.Header.Set("Authorization", "Bearer secret")
	if got := s.packPrincipal(r); got != want {
		t.Fatalf("token: %q, want %q", got, want)
	}
	if err := s.st.CreateWebSession(store.HashToken("sess"), alice.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/alice/app/git-upload-pack", nil)
	r.RemoteAddr = "192.0.2.7:4000"
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "sess"})
	if got := s.packPrincipal(r); got != want {
		t.Fatalf("session: %q, want %q", got, want)
	}
}

// stuckClient is a connection whose client sent the start of a request
// body and then stopped sending. Reads block until a read deadline is
// set; the response is discarded.
type stuckClient struct {
	header http.Header
	head   string // the part of the body that was sent
	cut    chan struct{}
	once   sync.Once
}

func (c *stuckClient) Header() http.Header         { return c.header }
func (c *stuckClient) WriteHeader(int)             {}
func (c *stuckClient) Write(b []byte) (int, error) { return len(b), nil }
func (c *stuckClient) Read(b []byte) (int, error) {
	if c.head != "" {
		n := copy(b, c.head)
		c.head = c.head[n:]
		return n, nil
	}
	<-c.cut
	return 0, os.ErrDeadlineExceeded
}
func (c *stuckClient) Close() error { return nil }
func (c *stuckClient) SetReadDeadline(time.Time) error {
	c.once.Do(func() { close(c.cut) })
	return nil
}

// limitedServer is a server with a pack limit and an empty alice/app.
func limitedServer(t *testing.T) *Server {
	t.Helper()
	s := busyServer(t)
	s.packs = packlimit.New(1, 0, 0, time.Second)
	if err := gitutil.InitBare(control.RepoDir(s.cfg.Server.Root, "alice", "app"), "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return s
}

// seed commits files of the given sizes, random and so incompressible,
// to alice/app's main and returns the commit.
func seed(t *testing.T, s *Server, sizes ...int) string {
	t.Helper()
	work := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	for i, n := range sizes {
		b := make([]byte, n)
		rand.Read(b)
		if err := os.WriteFile(filepath.Join(work, strconv.Itoa(i)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "seed")
	git("push", "-q", control.RepoDir(s.cfg.Server.Root, "alice", "app"), "main")
	return git("rev-parse", "HEAD")
}

// fetchBody is a protocol v0 request for sha's whole history.
func fetchBody(sha string) string {
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	return pkt("want "+sha+" side-band-64k ofs-delta\n") + "0000" + pkt("done\n")
}

// ended requires the handler to finish within limit and its slot to be
// free.
func ended(t *testing.T, s *Server, finished <-chan struct{}, limit time.Duration) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(limit):
		t.Fatal("fetch still running")
	}
	hold, err := s.packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatalf("slot not released after the kill: %v", err)
	}
	hold()
}

func stallAfter(t *testing.T, d time.Duration) {
	old := packlimit.StallDeadline
	packlimit.StallDeadline = d
	t.Cleanup(func() { packlimit.StallDeadline = old })
}

// A client that stops sending its body is cut after StallDeadline.
func TestFetchKilledWhenClientStopsSending(t *testing.T) {
	stallAfter(t, 200*time.Millisecond)
	s := limitedServer(t)
	// Half a pkt-line: git waits for the rest.
	c := &stuckClient{header: http.Header{}, head: "0032want 0123456789abcdef", cut: make(chan struct{})}
	r := httptest.NewRequest("POST", "/alice/app/git-upload-pack", c)
	r.SetPathValue("owner", "alice")
	r.SetPathValue("repo", "app")
	finished := make(chan struct{})
	go func() {
		s.uploadPack(c, r)
		close(finished)
	}()
	ended(t, s, finished, 5*time.Second)
}

// A client that leaves ends git at once, even while git is busy with
// neither its input nor its output: here a pack-objects hook that
// sleeps, with the whole request read and the response discarded.
func TestFetchKilledWhenClientLeaves(t *testing.T) {
	s := limitedServer(t)
	sha := seed(t, s, 10)
	hook := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "uploadpack.packObjectsHook")
	t.Setenv("GIT_CONFIG_VALUE_0", hook)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "POST", "/alice/app/git-upload-pack", strings.NewReader(fetchBody(sha)))
	r.SetPathValue("owner", "alice")
	r.SetPathValue("repo", "app")
	finished := make(chan struct{})
	go func() {
		s.uploadPack(httptest.NewRecorder(), r)
		close(finished)
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	// Unkilled, git runs until the hook's sleep ends.
	ended(t, s, finished, 5*time.Second)
}

// A client that stops reading the response is cut after StallDeadline:
// the write blocked on its full socket fails at the deadline set then.
func TestFetchKilledWhenClientStopsReading(t *testing.T) {
	stallAfter(t, 500*time.Millisecond)
	s := limitedServer(t)
	sizes := make([]int, 16)
	for i := range sizes {
		sizes[i] = 2 << 20
	}
	sha := seed(t, s, sizes...)
	finished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("owner", "alice")
		r.SetPathValue("repo", "app")
		s.uploadPack(w, r)
		close(finished)
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(4096)
	body := fetchBody(sha)
	fmt.Fprintf(conn, "POST /alice/app/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	// The 32MB pack cannot fit in the socket buffers; nothing is read.
	ended(t, s, finished, 20*time.Second)
}
