package httpd

import (
	"bytes"
	"encoding/json"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
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

// runControlJSON runs a command in JSON mode and returns its data object.
// In JSON mode a failure is an envelope carrying the message rather than
// stderr text, so both paths are read from the same envelope.
func (s *Server) runControlJSON(u store.User, argv []string) (data map[string]any, msg string, ok bool) {
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
		JSON:   true,
		ViaAPI: true,
	}
	code := control.Dispatch(ctx, argv)
	var env struct {
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	json.Unmarshal(stdout.Bytes(), &env)
	if code != protocol.ExitOK {
		m := env.Error
		if m == "" {
			m = strings.TrimSpace(stderr.String())
		}
		if m == "" {
			m = "the command failed"
		}
		return nil, m, false
	}
	return env.Data, "", true
}

// authorNames maps commit author addresses to account names for one
// request. A commit carries whatever name git was configured with; when
// the address is a verified address here, the account's own name is the
// truthful one to show, and it links somewhere.
type authorNames struct {
	st    *store.Store
	cache map[string]string
}

func (s *Server) authorNames() *authorNames {
	return &authorNames{st: s.st, cache: map[string]string{}}
}

// name returns the account name for an address, or the commit's own
// author name when no account has verified it.
func (a *authorNames) name(email, fallback string) string {
	if email == "" {
		return fallback
	}
	if got, ok := a.cache[email]; ok {
		if got == "" {
			return fallback
		}
		return got
	}
	name, _ := a.st.UsernameByVerifiedEmail(email)
	a.cache[email] = name
	if name == "" {
		return fallback
	}
	return name
}

// known reports whether the address belongs to an account, so callers can
// decide to link the name.
func (a *authorNames) known(email string) bool {
	if email == "" {
		return false
	}
	if got, ok := a.cache[email]; ok {
		return got != ""
	}
	name, _ := a.st.UsernameByVerifiedEmail(email)
	a.cache[email] = name
	return name != ""
}

// namedCommits rewrites listing authors to account names where the
// address is verified here.
func (s *Server) namedCommits(m map[string]gitutil.EntryCommit) map[string]gitutil.EntryCommit {
	names := s.authorNames()
	for k, c := range m {
		c.Author = names.name(c.Email, c.Author)
		m[k] = c
	}
	return m
}

// namedTip does the same for the single commit above a tree listing.
func (s *Server) namedTip(c gitutil.EntryCommit) gitutil.EntryCommit {
	c.Author = s.authorNames().name(c.Email, c.Author)
	return c
}
