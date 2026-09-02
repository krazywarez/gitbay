package control

import (
	"errors"
	"fmt"
	"io"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func newStoredToken() (token, hash string, err error) { return store.NewToken() }

func init() {
	register(Command{Path: []string{"web", "login"},
		Summary: "mint a one-time browser login URL",
		Usage:   "web login", Run: runWebLogin})
	register(Command{Path: []string{"web", "sessions", "list"},
		Summary: "list your browser sessions",
		Usage:   "web sessions list", ReadOnly: true, SSHOnly: true, Run: runWebSessionsList})
	register(Command{Path: []string{"web", "sessions", "revoke"},
		Summary: "end a browser session, or all of them",
		Usage:   "web sessions revoke <id>|--all", SSHOnly: true, Run: runWebSessionsRevoke})
}

func runWebSessionsList(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: web sessions list")
	}
	sessions, err := c.Store.ListWebSessions(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(sessions, func(w io.Writer) {
		for _, s := range sessions {
			fmt.Fprintf(w, "%s\tsince %s\tuntil %s\n", s.ID, s.CreatedAt, s.ExpiresAt)
		}
	})
}

func runWebSessionsRevoke(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: web sessions revoke <id>|--all")
	}
	if args[0] == "--all" {
		n, err := c.Store.RevokeAllWebSessions(c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		return c.emit(map[string]any{"revoked": n}, func(w io.Writer) {
			fmt.Fprintf(w, "revoked %d browser sessions\n", n)
		})
	}
	if err := c.Store.RevokeWebSession(c.User.ID, args[0]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no browser session %s on your account", args[0])
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"revoked": args[0]}, func(w io.Writer) {
		fmt.Fprintf(w, "revoked browser session %s\n", args[0])
	})
}

func runWebLogin(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: web login [--json]")
	}
	if c.Cfg.Web.Mode != "accounts" {
		return c.fail(protocol.ExitDenied,
			"this instance runs the web in view-only mode (web.mode = %q); there is nothing to log in to", c.Cfg.Web.Mode)
	}
	token, hash, err := newStoredToken()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.CreateLoginToken(c.User.ID, hash, 5*time.Minute); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	url := c.Cfg.Server.SiteURL + "/login?token=" + token
	return c.emit(map[string]string{"url": url, "expires_in": "5m"}, func(w io.Writer) {
		fmt.Fprintf(w, "open within 5 minutes (single use):\n%s\n", url)
	})
}
