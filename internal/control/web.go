package control

import (
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
