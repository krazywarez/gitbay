// Package control implements the forge control commands executed over SSH.
// Every command here is reachable from bare OpenSSH: argv in, JSON or plain
// text on stdout, diagnostics on stderr, exit code out.
package control

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

type Ctx struct {
	User   store.User
	Scope  string // scope of the key that authenticated this session
	Store  *store.Store
	Cfg    config.Config
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	JSON   bool
	// ViaAPI marks requests arriving over the HTTP token API. Some
	// commands (token management) are SSH-only: an API token must never
	// mint further credentials.
	ViaAPI bool
	// ReadOnly is set for read-scoped API tokens.
	ReadOnly bool
	// Source identifies the credential behind this session for the audit
	// log: an SSH key fingerprint, or "api" for token requests.
	Source string
}

type Command struct {
	Path []string // e.g. ["keys", "add"]
	// Summary is one line of prose: what the command does, no argument
	// syntax. Usage is the argument syntax, opening with the command path.
	// help renders them separately, so neither may carry the other's job.
	Summary    string
	Usage      string
	ReadsStdin bool
	ReadOnly   bool // safe for read-scoped API tokens
	SSHOnly    bool // refused over the HTTP API (credential minting)
	Run        func(c *Ctx, args []string) int
}

var registry []Command

func register(cmd Command) { registry = append(registry, cmd) }

// Commands returns the registry, for the bare-ssh reachability test.
func Commands() []Command { return registry }

// Lookup resolves argv to a command by longest path match, returning the
// command and the remaining arguments.
func Lookup(argv []string) (Command, []string, bool) {
	best := -1
	var found Command
	for _, cmd := range registry {
		if len(cmd.Path) <= len(argv) && slices.Equal(cmd.Path, argv[:len(cmd.Path)]) && len(cmd.Path) > best {
			best = len(cmd.Path)
			found = cmd
		}
	}
	if best < 0 {
		return Command{}, nil, false
	}
	return found, argv[best:], true
}

// Dispatch runs argv for an authenticated session. The dispatcher — not the
// handlers — enforces key scope: control commands require a full-scope key.
func Dispatch(c *Ctx, argv []string) int {
	if len(argv) == 0 {
		return c.fail(protocol.ExitUsage, "no command given; try: ssh <host> help")
	}
	cmd, rest, ok := Lookup(argv)
	if !ok {
		return c.fail(protocol.ExitUsage, "unknown command %q", argv[0])
	}
	// A runner-scoped key reaches the runner protocol and nothing else, so
	// the key a CI host holds cannot administer the instance.
	if c.Scope != "full" && !(c.Scope == "runner" && cmd.Path[0] == "runner") {
		return c.fail(protocol.ExitDenied, "this key's scope (%s) does not allow control commands", c.Scope)
	}
	if c.ViaAPI && cmd.SSHOnly {
		return c.fail(protocol.ExitDenied, "%s is only available over SSH", joinPath(cmd.Path))
	}
	if c.ReadOnly && !cmd.ReadOnly {
		return c.fail(protocol.ExitDenied, "this token is read-only; %s modifies state", joinPath(cmd.Path))
	}
	if c.User.Pending && !pendingAllowed(cmd.Path) {
		return c.fail(protocol.ExitDenied,
			"your account is not active yet: verify your email first (email verify <code>, or ask for the mail again with email add)")
	}
	// Strip the global --json flag wherever it appears.
	args := rest[:0:0]
	for _, a := range rest {
		if a == "--json" {
			c.JSON = true
			continue
		}
		args = append(args, a)
	}
	if !cmd.ReadsStdin {
		c.Stdin = emptyReader{}
	}
	code := cmd.Run(c, args)
	// Every successful mutating command lands in the audit log. Argv is
	// safe to record by construction: secrets travel on stdin, never as
	// arguments.
	if code == protocol.ExitOK && !cmd.ReadOnly {
		c.Store.Audit(c.User.ID, "cmd "+joinPath(cmd.Path), map[string]any{
			"argv":   args,
			"source": c.Source,
		})
	}
	return code
}

// pendingAllowed lists what an unverified self-registered account may do.
func pendingAllowed(path []string) bool {
	key := joinPath(path)
	return key == "email verify" || key == "email add" || key == "whoami" || key == "help"
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

// emit writes data as the command result: a JSON envelope under --json,
// otherwise via the plain formatter.
func (c *Ctx) emit(data any, plain func(w io.Writer)) int {
	// A nil slice would serialize as null; consumers should see [].
	if v := reflect.ValueOf(data); v.Kind() == reflect.Slice && v.IsNil() {
		data = reflect.MakeSlice(v.Type(), 0, 0).Interface()
	}
	if c.JSON {
		enc := json.NewEncoder(c.Stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(protocol.Envelope{ProtocolVersion: protocol.Version, Data: data}); err != nil {
			return protocol.ExitFailure
		}
		return protocol.ExitOK
	}
	plain(c.Stdout)
	return protocol.ExitOK
}

func (c *Ctx) fail(code int, format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	if c.JSON {
		enc := json.NewEncoder(c.Stdout)
		enc.SetEscapeHTML(false)
		enc.Encode(protocol.Envelope{ProtocolVersion: protocol.Version, Error: msg})
	} else {
		fmt.Fprintln(c.Stderr, msg)
	}
	return code
}

func init() {
	register(Command{
		Path:     []string{"help"},
		Summary:  "list available commands",
		Usage:    "help [<prefix>...]",
		ReadOnly: true,
		Run:      runHelp,
	})
}

// helpEntry is one row of the registry as help reports it.
type helpEntry struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Usage   string `json:"usage"`
}

// runHelp lists the registry, sorted, so a noun's commands sit together.
// A prefix narrows the listing and adds each command's argument syntax —
// the only place flags are written down. The unfiltered listing stays one
// line per command.
func runHelp(c *Ctx, args []string) int {
	prefix := joinPath(args)
	var matched []helpEntry
	for _, cmd := range registry {
		p := joinPath(cmd.Path)
		if prefix != "" && p != prefix && !strings.HasPrefix(p, prefix+" ") {
			continue
		}
		matched = append(matched, helpEntry{Path: p, Summary: cmd.Summary, Usage: cmd.Usage})
	}
	if len(matched) == 0 {
		return c.fail(protocol.ExitNotFound, "no command matches %q; try: help", prefix)
	}
	slices.SortFunc(matched, func(a, b helpEntry) int { return strings.Compare(a.Path, b.Path) })
	return c.emit(matched, func(w io.Writer) {
		for _, e := range matched {
			fmt.Fprintf(w, "%-24s %s\n", e.Path, e.Summary)
			if prefix != "" {
				fmt.Fprintf(w, "  %s\n", e.Usage)
			}
		}
	})
}

func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
