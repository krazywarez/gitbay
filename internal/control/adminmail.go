package control

import (
	"fmt"
	"io"
	"time"

	"gitbay.org/gitbay/internal/imapc"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"admin", "mail", "inbound", "check"},
		Summary:  "connect to the reply mailbox read-only and report what is waiting",
		Usage:    "admin mail inbound check",
		Examples: []string{"admin mail inbound check"},
		ReadOnly: true, Run: runAdminMailInboundCheck})
}

const unauthenticatedWarning = "trusted_authserv_id is unset: a reply's From is not checked against the mail host's DMARC and DKIM results"

// runAdminMailInboundCheck logs in to the [mail.inbound] mailbox and
// opens it with EXAMINE, which changes no flag, so a check never marks a
// reply seen before the poller reads it.
func runAdminMailInboundCheck(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) != 0 {
		return c.usage()
	}
	in := c.Cfg.Mail.Inbound
	type out struct {
		Enabled  bool   `json:"enabled"`
		Server   string `json:"server,omitempty"`
		Mailbox  string `json:"mailbox,omitempty"`
		Messages int    `json:"messages"`
		Unseen   int    `json:"unseen"`
		Warning  string `json:"warning,omitempty"`
	}
	if !in.Enabled {
		return c.emit(out{}, func(w io.Writer) {
			fmt.Fprintln(w, "inbound mail is off ([mail.inbound] enabled = false)")
		})
	}
	cl, n, err := imapc.Open(in, true, 30*time.Second)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%s: %v", in.Addr(), err)
	}
	defer cl.Close()
	unseen, err := cl.Unseen()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%s: %v", in.Addr(), err)
	}
	d := out{Enabled: true, Server: in.Addr(), Mailbox: in.MailboxName(), Messages: n, Unseen: len(unseen)}
	if in.TrustedAuthservID == "" {
		d.Warning = unauthenticatedWarning
		fmt.Fprintln(c.Stderr, "warning: "+d.Warning)
	}
	return c.emit(d, func(w io.Writer) {
		c.view(w).fields(
			"server", d.Server,
			"mailbox", d.Mailbox,
			"messages", fmt.Sprintf("%d", d.Messages),
			"unseen", fmt.Sprintf("%d", d.Unseen),
		)
	})
}
