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
	Path       []string // e.g. ["keys", "add"]
	Summary    string
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
	if c.Scope != "full" {
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
		ReadOnly: true,
		Run: func(c *Ctx, args []string) int {
			for _, cmd := range registry {
				fmt.Fprintf(c.Stdout, "%-24s %s\n", joinPath(cmd.Path), cmd.Summary)
			}
			return protocol.ExitOK
		},
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
