package control

import (
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"audit"},
		Summary: "instance audit log (admins)",
		Usage:   "audit [--actor <user>|-] [--action <prefix>] [--since <duration|date>] [--limit <n>]",
		Flags: []Flag{
			{"--actor", "<user>|-", "only entries by this user", ""},
			{"--action", "<prefix>", "only actions starting with this", ""},
			{"--since", "<duration|date>", "only entries after this", ""},
			{"--limit", "<n>", "rows to show", "100"},
		},
		Examples: []string{"audit --actor alice --since 24h"},
		ReadOnly: true, Run: runAudit})
}

func runAudit(c *Ctx, args []string) int {
	if !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "the audit log is for instance admins; ask one")
	}
	f := store.AuditFilter{Limit: 100}
	fl, err := c.parseArgs(args, flagSpec{Values: []string{"--limit", "--actor", "--action", "--since"}, MaxPos: 0, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if fl.Has("--limit") {
		n, err := strconv.Atoi(fl.Value("--limit"))
		if err != nil || n < 1 || n > 10000 {
			return c.fail(protocol.ExitUsage, "--limit must be 1 to 10000")
		}
		f.Limit = n
	}
	f.Actor, f.ActionPrefix = fl.Value("--actor"), fl.Value("--action")
	if fl.Has("--since") {
		t, ok := parseSince(fl.Value("--since"), time.Now())
		if !ok {
			return c.fail(protocol.ExitUsage, "--since takes a duration (30m, 24h, 7d) or a date (2026-09-01, RFC 3339)")
		}
		f.Since = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	entries, err := c.Store.AuditEntries(f)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emitView(entries, func(w io.Writer) {
		tb := c.table(w, "WHEN", "ACTOR", "ACTION", "DATA")
		for _, e := range entries {
			actor := e.Actor
			if actor == "" {
				actor = "-"
			}
			tb.row(cAge(e.CreatedAt), cText(actor), cText(e.Action), cFlex(e.Data))
		}
		tb.flush()
	}, func() screen {
		rows := make([]row, len(entries))
		for i, e := range entries {
			actor := e.Actor
			if actor == "" {
				actor = "-"
			}
			rows[i] = rowOf(cAge(e.CreatedAt), cText(actor), cText(e.Action), cFlex(keyValues(e.Data)))
		}
		return listScreen("Audit", rows, action{"Filter", []string{"audit", "--since", "24h"}})
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

// keyValues is an audit entry's JSON data as a terminal reads it:
// key=value pairs in key order, strings bare, arrays space-separated.
// Anything that is not a JSON object is returned as it is.
func keyValues(data string) string {
	var m map[string]any
	if json.Unmarshal([]byte(data), &m) != nil {
		return data
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + kvValue(m[k])
	}
	return strings.Join(parts, " ")
}

func kvValue(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = kvValue(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
