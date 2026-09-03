package control

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/webhook"
)

func init() {
	register(Command{Path: []string{"repo", "mirror", "add"},
		Summary:    "mirror to or from a remote",
		Usage:      "repo mirror add <owner/name> <https-url> --direction push|pull [--username <u>] [--token-stdin]",
		ReadsStdin: true, SSHOnly: true, Run: runMirrorAdd})
	register(Command{Path: []string{"repo", "mirror", "list"},
		Summary: "list mirrors with sync status",
		Usage:   "repo mirror list <owner/name>", ReadOnly: true, Run: runMirrorList})
	register(Command{Path: []string{"repo", "mirror", "remove"},
		Summary: "remove a mirror",
		Usage:   "repo mirror remove <owner/name> <id>", Run: runMirrorRemove})
	register(Command{Path: []string{"repo", "mirror", "sync"},
		Summary: "schedule an immediate sync",
		Usage:   "repo mirror sync <owner/name>", Run: runMirrorSync})
}

func runMirrorAdd(c *Ctx, args []string) int {
	var path, urlArg, direction, username string
	tokenStdin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--direction", "--username":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			if args[i] == "--direction" {
				direction = args[i+1]
			} else {
				username = args[i+1]
			}
			i++
		case "--token-stdin":
			tokenStdin = true
		default:
			if path == "" {
				path = args[i]
			} else if urlArg == "" {
				urlArg = args[i]
			} else {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
		}
	}
	if path == "" || urlArg == "" || (direction != "push" && direction != "pull") {
		return c.fail(protocol.ExitUsage, "usage: repo mirror add <owner/name> <https-url> --direction push|pull [--username <u>] [--token-stdin]")
	}
	// The worker's git process dials this URL from the server: same SSRF
	// surface as a webhook target, same rules.
	if err := webhook.ValidateURL(urlArg, c.Cfg.Webhooks.AllowLocal); err != nil {
		return c.failErr(err)
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	token := ""
	if tokenStdin {
		line, err := bufio.NewReader(io.LimitReader(c.Stdin, 4096)).ReadString('\n')
		if err != nil && line == "" {
			return c.fail(protocol.ExitUsage, "--token-stdin given but stdin held no token")
		}
		token = strings.TrimSpace(line)
	}
	id, err := c.Store.AddMirror(repo.ID, direction, urlArg, username, token)
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			return c.fail(protocol.ExitUsage, "that mirror already exists")
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	note := ""
	if direction == "pull" {
		note = "; local pushes are now refused — refs come from the upstream"
	}
	return c.emit(map[string]any{"id": id, "direction": direction, "url": urlArg}, func(w io.Writer) {
		fmt.Fprintf(w, "mirror %d added (%s %s)%s\n", id, direction, urlArg, note)
	})
}

func runMirrorList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo mirror list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	ms, err := c.Store.ListMirrors(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		ID        int64  `json:"id"`
		Direction string `json:"direction"`
		URL       string `json:"url"`
		Username  string `json:"username,omitempty"`
		Pending   bool   `json:"pending"`
		LastSync  string `json:"last_sync,omitempty"`
		LastError string `json:"last_error,omitempty"`
	}
	var ds []out
	for _, m := range ms {
		// The token never leaves the server, in any encoding.
		ds = append(ds, out{m.ID, m.Direction, m.URL, m.Username, m.Dirty, m.LastSync, m.LastError})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			status := "ok"
			if d.Pending {
				status = "pending"
			}
			if d.LastError != "" {
				status = "error: " + d.LastError
			}
			fmt.Fprintf(w, "%d\t%s\t%s\tlast %s\t%s\n", d.ID, d.Direction, d.URL, orDash(d.LastSync), status)
		}
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runMirrorRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo mirror remove <owner/name> <id>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad mirror id %q", args[1])
	}
	if err := c.Store.RemoveMirror(repo.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no mirror %d on %s", id, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"removed": id}, func(w io.Writer) {
		fmt.Fprintf(w, "removed mirror %d\n", id)
	})
}

func runMirrorSync(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo mirror sync <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.MarkMirrorsDirty(repo.ID, ""); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"sync": "scheduled"}, func(w io.Writer) {
		fmt.Fprintln(w, "sync scheduled; check repo mirror list for the outcome")
	})
}
