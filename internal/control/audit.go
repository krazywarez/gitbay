package control

import (
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"audit"},
		Summary: "instance audit log (admins): audit [--limit <n>]", ReadOnly: true, SSHOnly: true, Run: runAudit})
}

func runAudit(c *Ctx, args []string) int {
	if !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "the audit log is for instance admins")
	}
	limit := 100
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 && n <= 10000 {
				limit = n
			}
			i++
		} else {
			return c.fail(protocol.ExitUsage, "usage: audit [--limit <n>]")
		}
	}
	entries, err := c.Store.AuditEntries(limit)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(entries, func(w io.Writer) {
		for _, e := range entries {
			actor := e.Actor
			if actor == "" {
				actor = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.CreatedAt, actor, e.Action, e.Data)
		}
	})
}
