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
		Summary: "mint an API token (shown once): token create --name <n> [--scope full|read] [--ttl 30d|720h]",
		SSHOnly: true, Run: runTokenCreate})
	register(Command{Path: []string{"token", "list"},
		Summary: "list API tokens", ReadOnly: true, SSHOnly: true, Run: runTokenList})
	register(Command{Path: []string{"token", "revoke"},
		Summary: "revoke an API token by name: token revoke <name>",
		SSHOnly: true, Run: runTokenRevoke})
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
	name, scope, ttl := "", "full", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name", "--scope", "--ttl":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			switch args[i] {
			case "--name":
				name = args[i+1]
			case "--scope":
				scope = args[i+1]
			case "--ttl":
				ttl = args[i+1]
			}
			i++
		default:
			return c.fail(protocol.ExitUsage, "usage: token create --name <n> [--scope full|read] [--ttl 30d]")
		}
	}
	if name == "" || (scope != "full" && scope != "read") {
		return c.fail(protocol.ExitUsage, "usage: token create --name <n> [--scope full|read] [--ttl 30d]")
	}
	var expires *time.Time
	if ttl != "" {
		d, err := parseTTL(ttl)
		if err != nil {
			return c.fail(protocol.ExitUsage, "%v", err)
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
	if err := c.Store.CreateAPIToken(c.User.ID, name, store.HashToken(token), scope, expires); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
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
	}
	var ds []out
	for _, t := range tokens {
		ds = append(ds, out{t.Name, t.Scope, t.CreatedAt, t.ExpiresAt, t.LastUsedAt})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			exp := "never expires"
			if d.ExpiresAt != nil {
				exp = "expires " + d.ExpiresAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, d.Scope, exp)
		}
	})
}

func runTokenRevoke(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: token revoke <name>")
	}
	if err := c.Store.RevokeAPIToken(c.User.ID, args[0]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no token named %q", args[0])
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"revoked": args[0]}, func(w io.Writer) {
		fmt.Fprintf(w, "revoked %s\n", args[0])
	})
}
