package control

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"token", "create"},
		Summary: "mint an API token (shown once)",
		Usage:   "token create --name <n> [--scope read|full] [--ttl 30d|720h]",
		Flags: []Flag{
			{"--name", "<n>", "the token's name", ""},
			{"--scope", "read|full", "what the token may do; full is needed to change anything", "read"},
			{"--ttl", "30d|720h", "how long the token is valid; an expiring token cannot mint credentials", "never expires"},
		},
		Examples:        []string{"token create --name laptop --ttl 30d", "token create --name phone --scope full"},
		MintsCredential: true,
		Run:             runTokenCreate})
	register(Command{Path: []string{"token", "list"},
		Summary:  "list API tokens",
		Usage:    "token list",
		Examples: []string{"token list"}, ReadOnly: true, Run: runTokenList})
	register(Command{Path: []string{"token", "revoke"},
		Summary: "revoke an API token by name",
		Usage:   "token revoke <name> [--created]",
		Flags: []Flag{
			{"--created", "", "also revoke the tokens and keys it created, at any depth", ""},
		},
		Examples: []string{"token revoke laptop", "token revoke laptop --created"},
		Run:      runTokenRevoke})
}

// parseTTL accepts Go durations plus a day suffix ("30d").
func parseTTL(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("bad ttl %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func runTokenCreate(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--name", "--scope", "--ttl"}, MaxPos: 0, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	name, scope, ttl := f.Value("--name"), "read", f.Value("--ttl")
	if f.Has("--scope") {
		scope = f.Value("--scope")
	}
	if name == "" || (scope != "full" && scope != "read") {
		return c.usage()
	}
	var expires *time.Time
	if ttl != "" {
		d, err := parseTTL(ttl)
		if err != nil {
			return c.failInput(err)
		}
		t := time.Now().Add(d)
		expires = &t
	}
	raw, _, err := store.NewToken()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// The gb_ prefix makes leaked tokens findable by secret scanners.
	token := "gb_" + raw
	if err := c.Store.CreateAPIToken(c.User.ID, name, store.HashToken(token), scope, expires, c.TokenID); err != nil {
		return c.failErr(err)
	}
	type out struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
		Token string `json:"token"`
	}
	d := out{name, scope, token}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "token %q (%s) — shown once, store it now:\n%s\n", d.Name, d.Scope, d.Token)
	})
}

func runTokenList(c *Ctx, args []string) int {
	tokens, err := c.Store.ListAPITokens(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Name       string     `json:"name"`
		Scope      string     `json:"scope"`
		CreatedAt  string     `json:"created_at"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		LastUsedAt *time.Time `json:"last_used_at,omitempty"`
		CreatedBy  string     `json:"created_by,omitempty"`
	}
	var ds []out
	for _, t := range tokens {
		ds = append(ds, out{t.Name, t.Scope, t.CreatedAt, t.ExpiresAt, t.LastUsedAt, t.CreatedBy})
	}
	return c.emit(ds, func(w io.Writer) {
		tb := c.table(w, "NAME", "SCOPE", "EXPIRES")
		for _, d := range ds {
			exp := "never expires"
			if d.ExpiresAt != nil {
				ts := d.ExpiresAt.UTC().Format(time.RFC3339Nano)
				if c.Term.Cols == 0 {
					exp = "expires " + stamp(ts)
				} else {
					exp = "expires " + relAge(ts, termNow())
				}
			}
			tb.row(cRef(d.Name), cState(d.Scope), cText(exp))
		}
		tb.flush()
	})
}

func runTokenRevoke(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Bools: []string{"--created"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	name := f.pos(0)
	if name == "" {
		return c.usage()
	}
	withCreated := f.Has("--created")
	created, err := c.Store.RevokeAPIToken(c.User.ID, name, withCreated)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no token named %q", name)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Revoked        string        `json:"revoked"`
		Created        store.Created `json:"created"`
		CreatedRevoked bool          `json:"created_revoked"`
	}
	d := out{name, created, withCreated}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "revoked %s\n", name)
		if len(created.Tokens)+len(created.Keys) == 0 {
			return
		}
		if withCreated {
			fmt.Fprintln(w, "and what it created:")
		} else {
			fmt.Fprintln(w, "it created these, still in place:")
		}
		for _, n := range created.Tokens {
			fmt.Fprintf(w, "  token %s\n", n)
		}
		for _, fp := range created.Keys {
			fmt.Fprintf(w, "  key %s\n", fp)
		}
	})
}
