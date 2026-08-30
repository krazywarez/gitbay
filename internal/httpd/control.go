package httpd

import (
	"bytes"
	"encoding/json"
	"net/http"
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

// runControlStdin is runControl for the handful of commands whose input
// arrives on stdin: public keys, and review comment bodies. Neither is
// secret, and both are prose or paste rather than a flag value. Secrets,
// tokens and mirror credentials remain SSHOnly and are refused by the
// dispatcher.
func (s *Server) runControlStdin(u store.User, argv []string, stdin string) (msg string, ok bool) {
	var stdout, stderr bytes.Buffer
	ctx := &control.Ctx{
		User:   u,
		Source: "web",
		Scope:  "full",
		Store:  s.st,
		Cfg:    s.cfg,
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
		ViaAPI: true,
	}
	code := control.Dispatch(ctx, argv)
	m := strings.TrimSpace(stderr.String())
	if m == "" {
		m = strings.TrimSpace(stdout.String())
	}
	return m, code == protocol.ExitOK
}

// runControlInto runs a command in JSON mode and decodes its data into
// target. Read handlers use it so the web renders exactly what the CLI
// and the API return, rather than reaching past the registry into git.
func (s *Server) runControlInto(u store.User, argv []string, target any) (msg string, ok bool) {
	code, msg := s.dispatchInto(u, argv, target)
	return msg, code == protocol.ExitOK
}

// runControlIntoCode is runControlInto for handlers that have to tell
// "no such thing" from "that failed": a profile page 404s on the first
// and errors on the second.
func (s *Server) runControlIntoCode(u store.User, argv []string, target any) (code int, msg string) {
	return s.dispatchInto(u, argv, target)
}

func (s *Server) dispatchInto(u store.User, argv []string, target any) (int, string) {
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
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	json.Unmarshal(stdout.Bytes(), &env)
	if code != protocol.ExitOK {
		m := env.Error
		if m == "" {
			m = strings.TrimSpace(stderr.String())
		}
		return code, m
	}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, target); err != nil {
			return protocol.ExitFailure, "unreadable response"
		}
	}
	return protocol.ExitOK, ""
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

// account returns the account name behind an address, if any, so callers
// can link the displayed name to a profile.
func (a *authorNames) account(email string) (string, bool) {
	if email == "" {
		return "", false
	}
	if got, ok := a.cache[email]; ok {
		return got, got != ""
	}
	name, _ := a.st.UsernameByVerifiedEmail(email)
	a.cache[email] = name
	return name, name != ""
}

// namedCommit is a listing commit plus the account behind its author
// address, when there is one, so the name can link to a profile.
type namedCommit struct {
	gitutil.EntryCommit
	User string
}

// namedCommits rewrites listing authors to account names where the
// address is verified here.
func (s *Server) namedCommits(m map[string]gitutil.EntryCommit) map[string]namedCommit {
	names := s.authorNames()
	out := make(map[string]namedCommit, len(m))
	for k, c := range m {
		user, _ := names.account(c.Email)
		c.Author = names.name(c.Email, c.Author)
		out[k] = namedCommit{EntryCommit: c, User: user}
	}
	return out
}

// namedTip does the same for the single commit above a tree listing.
func (s *Server) namedTip(c gitutil.EntryCommit) namedCommit {
	names := s.authorNames()
	user, _ := names.account(c.Email)
	c.Author = names.name(c.Email, c.Author)
	return namedCommit{EntryCommit: c, User: user}
}

// webViewer is the account behind a page request, or the zero user when
// the instance serves the web without accounts.
func (s *Server) webViewer(r *http.Request) store.User {
	if s.cfg.Web.Mode != "accounts" {
		return store.User{}
	}
	return s.viewer(r)
}
