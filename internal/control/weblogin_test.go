package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// web login over SSH counts against the same hourly bound as the
// mailed links, since both insert into login_tokens (#278).
func TestWebLoginSharesTheLoginLinkLimit(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	for i := 0; i <= maxLoginLinksPerHour; i++ {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		c.Cfg.Web.Mode = "accounts"
		c.Cfg.Server.SiteURL = "https://gitbay.test"
		c.Cfg.Limits.WriteRate = -1
		code := Dispatch(c, []string{"web", "login"})
		switch {
		case i < maxLoginLinksPerHour && code != protocol.ExitOK:
			t.Fatalf("link %d: exit %d %s", i+1, code, errOut)
		case i == maxLoginLinksPerHour && (code != protocol.ExitDenied || !strings.Contains(errOut.String(), "login links")):
			t.Fatalf("link %d: exit %d %q, want refused", i+1, code, errOut)
		}
	}
}
