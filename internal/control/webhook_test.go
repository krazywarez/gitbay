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
