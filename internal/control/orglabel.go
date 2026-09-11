package control

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"org", "label", "set"},
		Summary: "create an org label every org repository sees, or set its colour; folds in same-named repo labels",
		Usage:   "org label set <org> <label> [--color rrggbb|'']", Run: runOrgLabelSet})
	register(Command{Path: []string{"org", "label", "list"},
		Summary: "list an org's labels with use across the repositories you can read",
		Usage:   "org label list <org>", ReadOnly: true, Run: runOrgLabelList})
	register(Command{Path: []string{"org", "label", "remove"},
		Summary: "remove an org label from the org and from every issue under it",
		Usage:   "org label remove <org> <label>", Run: runOrgLabelRemove})
	register(Command{Path: []string{"org", "milestone", "create"},
		Summary: "create an org milestone spanning every org repository; folds in same-titled repo milestones",
		Usage:   "org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]", Run: runOrgMilestoneCreate})
	register(Command{Path: []string{"org", "milestone", "list"},
		Summary: "list an org's milestones with progress across the repositories you can read",
		Usage:   "org milestone list <org> [--state open|closed|all]", ReadOnly: true, Run: runOrgMilestoneList})
	register(Command{Path: []string{"org", "milestone", "close"},
		Summary: "close an org milestone",
		Usage:   "org milestone close <org> <title>", Run: runOrgMilestoneClose})
	register(Command{Path: []string{"org", "milestone", "reopen"},
		Summary: "reopen an org milestone",
		Usage:   "org milestone reopen <org> <title>", Run: runOrgMilestoneReopen})
}

// orgReader resolves an org for a read of its labels or milestones.
// Members read; an outsider reads when some repository under the org is
// readable, and is refused rather than told the org is missing otherwise,
// since an org's existence is public anyway. The readable ids come back
// because every read counts over them.
func orgReader(c *Ctx, name string) (store.Org, []int64, int) {
	org, err := c.Store.OrgByName(name)
	if errors.Is(err, store.ErrNotFound) {
		return org, nil, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	readable, err := ReadableOrgRepoIDs(c.Store, c.User, org.ID)
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	role, err := c.Store.OrgRole(org.ID, c.User.ID)
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	if role == "" && len(readable) == 0 {
		return org, nil, c.fail(protocol.ExitDenied, "labels and milestones of %s are visible to its members", name)
	}
	return org, readable, -1
}

func runOrgLabelSet(c *Ctx, args []string) int {
	const usage = "usage: org label set <org> <label> [--color rrggbb|'']"
	f, err := parseFlags(args, flagSpec{Values: []string{"--color"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	orgName, name := f.pos(0), f.pos(1)
	color, colorSet := strings.ToLower(f.Value("--color")), f.Has("--color")
	if orgName == "" || name == "" {
		return c.fail(protocol.ExitUsage, usage)
	}
	if name == "" || len(name) > 50 {
		return c.fail(protocol.ExitUsage, "a label is 1 to 50 characters")
	}
	if colorSet && color != "" {
		if !labelColorPat.MatchString(color) {
			return c.fail(protocol.ExitUsage, "--color takes rrggbb (with or without #), or '' to clear")
		}
		color = "#" + strings.TrimPrefix(color, "#")
	}
	org, code := orgAdmin(c, orgName)
	if code >= 0 {
		return code
	}
	if !colorSet {
		// Keep the colour it has, if any; this is "make sure it exists".
		if labels, err := c.Store.ListOrgLabels(org.ID, nil); err == nil {
			for _, l := range labels {
				if l.Name == name {
					color = l.Color
				}
			}
		}
	}
	folded, err := c.Store.SetOrgLabel(org.ID, name, color)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(struct {
		Name   string `json:"name"`
		Color  string `json:"color,omitempty"`
		Folded int    `json:"folded"`
	}{name, color, folded}, func(w io.Writer) {
		if color == "" {
			fmt.Fprintf(w, "org label %s on %s, no colour set", name, org.Name)
		} else {
			fmt.Fprintf(w, "org label %s on %s is %s", name, org.Name, color)
		}
		if folded > 0 {
			fmt.Fprintf(w, "; folded in %d repositor%s", folded, map[bool]string{true: "y", false: "ies"}[folded == 1])
		}
		fmt.Fprintln(w)
	})
}

func runOrgLabelList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org label list <org>")
	}
	org, readable, code := orgReader(c, args[0])
	if code >= 0 {
		return code
	}
	labels, err := c.Store.ListOrgLabels(org.ID, readable)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(labels, func(w io.Writer) {
		for _, l := range labels {
			fmt.Fprintf(w, "%s\t%s\t%d\n", l.Name, l.Color, l.Issues)
		}
	})
}

func runOrgLabelRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org label remove <org> <label>")
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	if err := c.Store.DeleteOrgLabel(org.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no org label %q on %s", args[1], org.Name)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed org label %s from %s\n", args[1], org.Name)
	})
}

func runOrgMilestoneCreate(c *Ctx, args []string) int {
	const usage = "usage: org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]"
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--due"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	orgName, title, description, due := f.pos(0), f.pos(1), f.Value("--description"), f.Value("--due")
	if orgName == "" || title == "" {
		return c.fail(protocol.ExitUsage, usage)
	}
	if due != "" && !duePat.MatchString(due) {
		return c.fail(protocol.ExitUsage, "--due must be YYYY-MM-DD")
	}
	org, code := orgAdmin(c, orgName)
	if code >= 0 {
		return code
	}
	_, folded, err := c.Store.CreateOrgMilestone(org.ID, title, description, due)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(struct {
		Milestone string `json:"milestone"`
		Folded    int    `json:"folded"`
	}{title, folded}, func(w io.Writer) {
		fmt.Fprintf(w, "created org milestone %q on %s", title, org.Name)
		if folded > 0 {
			fmt.Fprintf(w, "; folded in %d repositor%s", folded, map[bool]string{true: "y", false: "ies"}[folded == 1])
		}
		fmt.Fprintln(w)
	})
}

func runOrgMilestoneList(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--state"}, MaxPos: 1, Usage: "org milestone list <org> [--state open|closed|all]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	state, orgName := "open", f.pos(0)
	if f.Has("--state") {
		state = f.Value("--state")
	}
	if orgName == "" || (state != "open" && state != "closed" && state != "all") {
		return c.fail(protocol.ExitUsage, "usage: org milestone list <org> [--state open|closed|all]")
	}
	org, readable, code := orgReader(c, orgName)
	if code >= 0 {
		return code
	}
	ms, err := c.Store.ListOrgMilestones(org.ID, state, readable)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitMilestones(c, ms)
}

func runOrgMilestoneClose(c *Ctx, args []string) int  { return setOrgMilestoneState(c, args, "closed") }
func runOrgMilestoneReopen(c *Ctx, args []string) int { return setOrgMilestoneState(c, args, "open") }

func setOrgMilestoneState(c *Ctx, args []string, state string) int {
	verb := "close"
	if state == "open" {
		verb = "reopen"
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org milestone %s <org> <title>", verb)
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	m, err := c.Store.OrgMilestoneByTitle(org.ID, args[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no org milestone %q on %s", args[1], org.Name)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.SetMilestoneState(m.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"milestone": m.Title, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%sd org milestone %q on %s\n", verb, m.Title, org.Name)
	})
}
