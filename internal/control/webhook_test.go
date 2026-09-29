package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// TestWebhookAddRefusedURLIsAFailure: a URL the server refuses after
// parsing it — here one that resolves to a loopback address — is the
// caller's value being rejected, not a malformed command line. Exit 1
// carries the sentence verbatim to every client; exit 2 reads as an
// app bug and is shown as a generic error (#187).
func TestWebhookAddRefusedURLIsAFailure(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	var out, errOut bytes.Buffer
	c := &Ctx{
		User:   store.User{ID: uid, Username: "alice"},
		Store:  st,
		Scope:  "full",
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
	}
	code := Dispatch(c, []string{"webhook", "add", repo.Path(), "http://127.0.0.1/hook"})
	if code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d (%s)", code, protocol.ExitFailure, strings.TrimSpace(errOut.String()))
	}
	if !strings.Contains(errOut.String(), "private or local address") {
		t.Errorf("message %q lacks the server's reason", strings.TrimSpace(errOut.String()))
	}
	// The shape of the command line is still a usage error.
	out.Reset()
	errOut.Reset()
	if code := Dispatch(c, []string{"webhook", "add", repo.Path()}); code != protocol.ExitUsage {
		t.Errorf("missing url: exit %d, want %d", code, protocol.ExitUsage)
	}
}

// The signing secret arrives on stdin with --secret -, like a build
// secret: a value on the command line is refused before anything is
// stored, since argv shows in /proc and in shell history (#284).
func TestWebhookAddSecretFromStdin(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	run := func(stdin string, argv ...string) (string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		c.Cfg.Limits.WriteRate = -1
		c.Cfg.Webhooks.AllowLocal = true
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return errOut.String(), code
	}
	msg, code := run("", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "s3cret")
	if code != protocol.ExitUsage || !strings.Contains(msg, "--secret -") {
		t.Fatalf("literal secret: exit %d, %q", code, msg)
	}
	if msg, code := run("", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "-"); code != protocol.ExitUsage || !strings.Contains(msg, "no secret on stdin") {
		t.Fatalf("empty stdin: exit %d, %q", code, msg)
	}
	if hooks, err := st.ListWebhooks(repo.ID); err != nil || len(hooks) != 0 {
		t.Fatalf("a refused add stored %+v (%v)", hooks, err)
	}
	if msg, code := run("s3cret\n", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "-"); code != protocol.ExitOK {
		t.Fatalf("piped secret: exit %d, %q", code, msg)
	}
	// Without --secret nothing reads stdin and the hook is unsigned.
	if msg, code := run("not a secret\n", "webhook", "add", repo.Path(), "http://127.0.0.1/other"); code != protocol.ExitOK {
		t.Fatalf("no secret: exit %d, %q", code, msg)
	}
	hooks, err := st.ListWebhooks(repo.ID)
	if err != nil || len(hooks) != 2 {
		t.Fatalf("hooks: %+v %v", hooks, err)
	}
	if hooks[0].Secret != "s3cret" || hooks[1].Secret != "" {
		t.Fatalf("secrets: %q, %q", hooks[0].Secret, hooks[1].Secret)
	}
}
