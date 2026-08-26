package httpd

import (
	"bytes"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// runControl executes a control command as the browser session's user,
// through the same registry the CLI and the JSON API reach. Web writes
// never reimplement command logic — merge gates, review rules, and audit
// entries stay in one place — so the surfaces cannot drift apart.
//
// ViaAPI is set, which refuses SSHOnly commands: anything whose input is a
// credential (secrets, mirror tokens, session minting) stays on SSH.
func (s *Server) runControl(u store.User, argv []string) (out string, msg string, ok bool) {
	var stdout, stderr bytes.Buffer
	ctx := &control.Ctx{
		User:   u,
		Source: "web",
		Scope:  "full",
		Store:  s.st,
		Cfg:    s.cfg,
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		ViaAPI: true,
	}
	code := control.Dispatch(ctx, argv)
	m := strings.TrimSpace(stderr.String())
	if m == "" {
		m = strings.TrimSpace(stdout.String())
	}
	return stdout.String(), m, code == protocol.ExitOK
}
