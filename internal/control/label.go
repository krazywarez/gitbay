package control

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"label", "list"},
		Summary: "list a repository's issue labels with colour and use",
		Usage:   "label list <owner/name>", ReadOnly: true, Run: runLabelList})
	register(Command{Path: []string{"label", "set"},
		Summary: "create a label or set its colour",
		Usage:   "label set <owner/name> <label> [--color rrggbb|'']", Run: runLabelSet})
	register(Command{Path: []string{"label", "remove"},
		Summary: "remove a label from the repository and from every issue",
		Usage:   "label remove <owner/name> <label>", Run: runLabelRemove})
}

// A colour is six hex digits, with or without the hash: over bare ssh a
// bare # starts a comment for the remote tokenizer, so the form without
// it is the one that types cleanly there.
var labelColorPat = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

func runLabelList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: label list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	readable, err := ReadableScope(c.Store, c.User, repo)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	labels, err := c.Store.ListLabels(repo, readable)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(labels, func(w io.Writer) {
		for _, l := range labels {
			fmt.Fprintf(w, "%s\t%s\t%d%s\n", l.Name, l.Color, l.Issues, map[bool]string{true: "\torg"}[l.Org])
		}
	})
}

func runLabelSet(c *Ctx, args []string) int {
	const usage = "usage: label set <owner/name> <label> [--color #rrggbb|'']"
	f, err := parseFlags(args, flagSpec{Values: []string{"--color"}, MaxPos: -1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	rest := f.Pos
	color, colorSet := strings.ToLower(f.Value("--color")), f.Has("--color")
	if len(rest) != 2 {
		return c.fail(protocol.ExitUsage, usage)
	}
	if colorSet && color != "" {
		if !labelColorPat.MatchString(color) {
			return c.fail(protocol.ExitUsage, "--color takes rrggbb (with or without #), or '' to clear")
		}
		color = "#" + strings.TrimPrefix(color, "#")
	}
	repo, code := resolveRepo(c, rest[0], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	name := rest[1]
	if name == "" || len(name) > 50 {
		return c.fail(protocol.ExitUsage, "a label is 1 to 50 characters")
	}
	if !colorSet {
		// Keep the colour it has, if any; this is "make sure it exists".
		if l, err := c.Store.LabelByName(repo, name); err == nil && !l.Org {
			color = l.Color
		}
	}
	if err := c.Store.SetLabel(repo, name, color); err != nil {
		if errors.Is(err, store.ErrOrgScoped) {
			return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "label", name, "set"))
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(store.Label{Name: name, Color: color}, func(w io.Writer) {
		if color == "" {
			fmt.Fprintf(w, "label %s, no colour set\n", name)
			return
		}
		fmt.Fprintf(w, "label %s is %s\n", name, color)
	})
}

func runLabelRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: label remove <owner/name> <label>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if err := c.Store.DeleteLabel(repo, args[1]); err != nil {
		if errors.Is(err, store.ErrOrgScoped) {
			return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "label", args[1], "remove"))
		}
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no label %q in %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed label %s\n", args[1])
	})
}
