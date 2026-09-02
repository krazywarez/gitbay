package control

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"audit"},
		Summary:  "instance audit log (admins)",
		Usage:    "audit [--actor <user>|-] [--action <prefix>] [--since <duration|date>] [--limit <n>]",
		ReadOnly: true, SSHOnly: true, Run: runAudit})
}

const auditUsage = "usage: audit [--actor <user>|-] [--action <prefix>] [--since <duration|date>] [--limit <n>]"

func runAudit(c *Ctx, args []string) int {
	if !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "the audit log is for instance admins")
	}
	f := store.AuditFilter{Limit: 100}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return c.fail(protocol.ExitUsage, auditUsage)
		}
		v := args[i+1]
		switch args[i] {
		case "--limit":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 10000 {
				return c.fail(protocol.ExitUsage, "--limit must be 1 to 10000")
			}
			f.Limit = n
		case "--actor":
			f.Actor = v
		case "--action":
			f.ActionPrefix = v
		case "--since":
			t, ok := parseSince(v, time.Now())
			if !ok {
				return c.fail(protocol.ExitUsage, "--since takes a duration (30m, 24h, 7d) or a date (2026-09-01, RFC 3339)")
			}
			f.Since = t.UTC().Format("2006-01-02T15:04:05.000Z")
		default:
			return c.fail(protocol.ExitUsage, auditUsage)
		}
		i++
	}
	entries, err := c.Store.AuditEntries(f)
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

// parseSince reads --since as a duration back from now (with a d suffix
// for days, which time.ParseDuration lacks) or as a date or RFC 3339
// timestamp.
func parseSince(v string, now time.Time) (time.Time, bool) {
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), true
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
