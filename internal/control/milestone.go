package control

import (
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"milestone", "create"},
		Summary: "create a milestone",
		Usage:   "milestone create <owner/name> <title> [--description <d>] [--due YYYY-MM-DD]", Run: runMilestoneCreate})
	register(Command{Path: []string{"milestone", "list"},
		Summary: "list milestones with progress",
		Usage:   "milestone list <owner/name> [--state open|closed|all]", ReadOnly: true, Run: runMilestoneList})
	register(Command{Path: []string{"milestone", "close"},
		Summary: "close a milestone",
		Usage:   "milestone close <owner/name> <title>", Run: runMilestoneClose})
	register(Command{Path: []string{"milestone", "reopen"},
		Summary: "reopen a milestone",
		Usage:   "milestone reopen <owner/name> <title>", Run: runMilestoneReopen})
	register(Command{Path: []string{"issue", "milestone"},
		Summary: "set or clear an issue's milestone",
		Usage:   "issue milestone <owner/name> <n> <title|none>", Run: runIssueMilestone})
	register(Command{Path: []string{"mr", "milestone"},
		Summary: "set or clear an MR's milestone",
		Usage:   "mr milestone <owner/name> <n> <title|none>", Run: runMRMilestone})
	register(Command{Path: []string{"issue", "templates"},
		Summary: "list issue templates (.gitbay/issue-template*.md)",
		Usage:   "issue templates <owner/name>", ReadOnly: true, Run: runIssueTemplates})
}

var duePat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func runMilestoneCreate(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--due"}, MaxPos: 2,
		Usage: "milestone create <owner/name> <title> [--description <d>] [--due YYYY-MM-DD]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, title, description, due := f.pos(0), f.pos(1), f.Value("--description"), f.Value("--due")
	if path == "" || title == "" {
		return c.fail(protocol.ExitUsage, "usage: milestone create <owner/name> <title> [--description <d>] [--due YYYY-MM-DD]")
	}
	if due != "" && !duePat.MatchString(due) {
		return c.fail(protocol.ExitUsage, "--due must be YYYY-MM-DD")
	}
	repo, code := resolveRepo(c, path, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if _, err := c.Store.CreateMilestone(repo.ID, title, description, due); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]string{"milestone": title}, func(w io.Writer) {
		fmt.Fprintf(w, "created milestone %q on %s\n", title, repo.Path())
	})
}

func runMilestoneList(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--state"}, MaxPos: 1, Usage: "milestone list <owner/name> [--state open|closed|all]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	state, path := "open", f.pos(0)
	if f.Has("--state") {
		state = f.Value("--state")
	}
	if path == "" || (state != "open" && state != "closed" && state != "all") {
		return c.fail(protocol.ExitUsage, "usage: milestone list <owner/name> [--state open|closed|all]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	ms, err := c.Store.ListMilestones(repo.ID, state)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Due         string `json:"due,omitempty"`
		State       string `json:"state"`
		Open        int    `json:"open"`
		Closed      int    `json:"closed"`
	}
	var ds []out
	for _, m := range ms {
		ds = append(ds, out{m.Title, m.Description, m.DueDate, m.State, m.OpenItems, m.ClosedItems})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			due := d.Due
			if due == "" {
				due = "-"
			}
			fmt.Fprintf(w, "%s\t%s\tdue %s\t%d open, %d closed\n", d.Title, d.State, due, d.Open, d.Closed)
		}
	})
}

func runMilestoneClose(c *Ctx, args []string) int  { return setMilestoneState(c, args, "closed") }
func runMilestoneReopen(c *Ctx, args []string) int { return setMilestoneState(c, args, "open") }

func setMilestoneState(c *Ctx, args []string, state string) int {
	verb := "close"
	if state == "open" {
		verb = "reopen"
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: milestone %s <owner/name> <title>", verb)
	}
	repo, code := resolveRepo(c, args[0], policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	m, err := c.Store.MilestoneByTitle(repo.ID, args[1])
	if err != nil {
		return milestoneErr(c, repo, args[1], err)
	}
	if err := c.Store.SetMilestoneState(m.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"milestone": m.Title, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%sd milestone %q on %s\n", verb, m.Title, repo.Path())
	})
}

func milestoneErr(c *Ctx, repo store.Repo, title string, err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no milestone %q in %s", title, repo.Path())
	}
	return c.fail(protocol.ExitFailure, "%v", err)
}

func runIssueMilestone(c *Ctx, args []string) int {
	repo, issue, code := issueRef(c, args, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: issue milestone <owner/name> <n> <title|none>")
	}
	return setItemMilestone(c, repo, "issue", issue.Number, args[2], func(id int64) error {
		return c.Store.SetIssueMilestone(issue.ID, id)
	})
}

func runMRMilestone(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: mr milestone <owner/name> <n> <title|none>")
	}
	return setItemMilestone(c, repo, "mr", mr.Number, args[2], func(id int64) error {
		return c.Store.SetMRMilestone(mr.ID, id)
	})
}

// noun and number name what the milestone was set on, for the event.
func setItemMilestone(c *Ctx, repo store.Repo, noun string, number int64, title string, set func(int64) error) int {
	var id int64
	if title != "none" {
		m, err := c.Store.MilestoneByTitle(repo.ID, title)
		if err != nil {
			return milestoneErr(c, repo, title, err)
		}
		id = m.ID
	}
	if err := set(id); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	cleared := title
	if cleared == "none" {
		cleared = ""
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, noun+".milestoned",
		fmt.Sprintf(`{"number":%d,"milestone":%q}`, number, cleared))
	if title == "none" {
		return c.emit(map[string]string{"milestone": ""}, func(w io.Writer) {
			fmt.Fprintln(w, "milestone cleared")
		})
	}
	return c.emit(map[string]string{"milestone": title}, func(w io.Writer) {
		fmt.Fprintf(w, "milestone set to %q\n", title)
	})
}

// runIssueTemplates lists .gitbay/issue-template*.md at the default branch.
func runIssueTemplates(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: issue templates <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	ts := IssueTemplates(dir, repo.DefaultBranch)
	return c.emit(ts, func(w io.Writer) {
		for _, t := range ts {
			fmt.Fprintln(w, t.Name)
		}
	})
}

type IssueTemplate struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// IssueTemplates reads .gitbay/issue-template*.md from ref. Missing
// directory or unreadable files yield an empty list, never an error.
func IssueTemplates(dir, ref string) []IssueTemplate {
	entries, err := gitutil.ListTree(dir, ref, ".gitbay")
	if err != nil {
		return nil
	}
	var out []IssueTemplate
	for _, e := range entries {
		if e.Type != "blob" || !strings.HasPrefix(e.Name, "issue-template") || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		raw, err := gitutil.ReadBlob(dir, ref, path.Join(".gitbay", e.Name), maxBodyBytes)
		if err != nil {
			continue
		}
		out = append(out, IssueTemplate{Name: e.Name, Body: string(raw)})
	}
	return out
}
