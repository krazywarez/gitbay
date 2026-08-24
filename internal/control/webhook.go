package control

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/webhook"
)

func init() {
	register(Command{Path: []string{"webhook", "add"},
		Summary: "add a webhook: webhook add <owner/name> <url> [--secret <s>] [--events push,issue.created|*]", Run: runWebhookAdd})
	register(Command{Path: []string{"webhook", "list"},
		Summary: "list webhooks: webhook list <owner/name>", ReadOnly: true, Run: runWebhookList})
	register(Command{Path: []string{"webhook", "remove"},
		Summary: "remove a webhook: webhook remove <owner/name> <id>", Run: runWebhookRemove})
	register(Command{Path: []string{"webhook", "deliveries"},
		Summary: "recent deliveries: webhook deliveries <owner/name> [--limit n]", ReadOnly: true, Run: runWebhookDeliveries})
	register(Command{Path: []string{"webhook", "redeliver"},
		Summary: "queue a delivery again: webhook redeliver <owner/name> <delivery-id>", Run: runWebhookRedeliver})
}

func runWebhookAdd(c *Ctx, args []string) int {
	var path, url, secret string
	events := "*"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--secret", "--events":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			if args[i] == "--secret" {
				secret = args[i+1]
			} else {
				events = args[i+1]
			}
			i++
		default:
			if path == "" {
				path = args[i]
			} else if url == "" {
				url = args[i]
			} else {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
		}
	}
	if path == "" || url == "" {
		return c.fail(protocol.ExitUsage, "usage: webhook add <owner/name> <url> [--secret <s>] [--events <k1,k2>|*]")
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := webhook.ValidateURL(url, c.Cfg.Webhooks.AllowLocal); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	id, err := c.Store.AddWebhook(repo.ID, url, secret, events)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"id": id, "url": url, "events": events}, func(w io.Writer) {
		fmt.Fprintf(w, "webhook %d added for %s (%s)\n", id, repo.Path(), events)
	})
}

func runWebhookList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: webhook list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	hooks, err := c.Store.ListWebhooks(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		ID     int64  `json:"id"`
		URL    string `json:"url"`
		Events string `json:"events"`
		Active bool   `json:"active"`
		Secret bool   `json:"has_secret"`
	}
	var ds []out
	for _, h := range hooks {
		ds = append(ds, out{h.ID, h.URL, h.Events, h.Active, h.Secret != ""})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%d\t%s\t%s\n", d.ID, d.URL, d.Events)
		}
	})
}

func runWebhookRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: webhook remove <owner/name> <id>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad webhook id %q", args[1])
	}
	if err := c.Store.RemoveWebhook(repo.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no webhook %d on %s", id, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"removed": id}, func(w io.Writer) {
		fmt.Fprintf(w, "removed webhook %d\n", id)
	})
}

func runWebhookDeliveries(c *Ctx, args []string) int {
	limit := 20
	var path string
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" {
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--limit requires a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > 200 {
				return c.fail(protocol.ExitUsage, "--limit must be 1..200")
			}
			limit = n
			i++
			continue
		}
		if path != "" {
			return c.fail(protocol.ExitUsage, "usage: webhook deliveries <owner/name> [--limit n]")
		}
		path = args[i]
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: webhook deliveries <owner/name> [--limit n]")
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	ds, err := c.Store.ListDeliveries(repo.ID, limit)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		ID         int64  `json:"id"`
		URL        string `json:"url"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Attempts   int    `json:"attempts"`
		LastStatus int    `json:"last_status,omitempty"`
		LastError  string `json:"last_error,omitempty"`
	}
	var rows []out
	for _, d := range ds {
		rows = append(rows, out{d.ID, d.URL, d.EventKind, d.Status, d.Attempts, d.LastStatus, d.LastError})
	}
	return c.emit(rows, func(w io.Writer) {
		for _, d := range rows {
			extra := ""
			if d.LastError != "" {
				extra = "\t" + d.LastError
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s (%d attempts)%s\n", d.ID, d.Event, d.URL, d.Status, d.Attempts, extra)
		}
	})
}

func runWebhookRedeliver(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: webhook redeliver <owner/name> <delivery-id>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad delivery id %q", args[1])
	}
	if err := c.Store.Redeliver(repo.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no delivery %d on %s", id, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"requeued": id}, func(w io.Writer) {
		fmt.Fprintf(w, "delivery %d requeued\n", id)
	})
}
