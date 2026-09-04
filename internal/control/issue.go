package control

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

const maxBodyBytes = 64 << 10

func init() {
	register(Command{Path: []string{"issue", "create"},
		Summary:    "open an issue",
		Usage:      "issue create <owner/name> --title <t> [--body <b> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runIssueCreate})
	register(Command{Path: []string{"issue", "list"},
		Summary: "list issues",
		Usage:   "issue list <owner/name> [--state open|closed|all] [--label <l>] [--assignee <user>] [--author <user>] [--milestone <title>|none] [--search <text>] [--limit <n>] [--cursor <c>]", ReadOnly: true, Run: runIssueList})
	register(Command{Path: []string{"issue", "show"},
		Summary: "show an issue with comments",
		Usage:   "issue show <owner/name> <n>", ReadOnly: true, Run: runIssueShow})
	register(Command{Path: []string{"issue", "edit"},
		Summary:    "edit title or body",
		Usage:      "issue edit <owner/name> <n> [--title <t>] [--body <b> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runIssueEdit})
	register(Command{Path: []string{"issue", "comment"},
		Summary:    "comment",
		Usage:      "issue comment <owner/name> <n> [--message <m> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runIssueComment})
	register(Command{Path: []string{"issue", "close"},
		Summary: "close an issue",
		Usage:   "issue close <owner/name> <n>", Run: runIssueClose})
	register(Command{Path: []string{"issue", "reopen"},
		Summary: "reopen an issue",
		Usage:   "issue reopen <owner/name> <n>", Run: runIssueReopen})
	register(Command{Path: []string{"issue", "label"},
		Summary: "labels",
		Usage:   "issue label <owner/name> <n> [--add <l>]... [--remove <l>]...", Run: runIssueLabel})
	register(Command{Path: []string{"issue", "assign"},
		Summary: "assignees",
		Usage:   "issue assign <owner/name> <n> [--add <user>]... [--remove <user>]...", Run: runIssueAssign})
}

// issueArgs parses "<owner/name> <n>" plus flags handled by the caller.
func issueRef(c *Ctx, args []string, perm func(store.User, store.Repo, string) bool) (store.Repo, store.Issue, int) {
	repo, n, code := refArgs(c, args, perm, "issue")
	if code >= 0 {
		return repo, store.Issue{}, code
	}
	issue, err := c.Store.IssueByNumber(repo.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		return repo, issue, c.fail(protocol.ExitNotFound, "issue #%d not found in %s", n, repo.Path())
	}
	if err != nil {
		return repo, issue, c.fail(protocol.ExitFailure, "%v", err)
	}
	return repo, issue, -1
}

// bodyFrom resolves --body/--message inline text or --file - (stdin).
func bodyFrom(c *Ctx, inline, file string) (string, error) {
	if inline != "" && file != "" {
		return "", errors.New("give either an inline message or --file -, not both")
	}
	if file != "" {
		if file != "-" {
			return "", errors.New("--file only supports - (stdin) over ssh")
		}
		raw, err := io.ReadAll(io.LimitReader(c.Stdin, maxBodyBytes))
		return string(raw), err
	}
	return inline, nil
}

// markupFormat normalizes a --format value. Empty means the caller did not ask,
// which the caller turns into "md" on create or "unchanged" on edit.
func markupFormat(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "md", "markdown":
		return "md", nil
	case "org", "org-mode":
		return "org", nil
	}
	return "", fmt.Errorf("unknown --format %q (want md or org)", v)
}

type issueOut struct {
	Number     int64    `json:"number"`
	Title      string   `json:"title"`
	State      string   `json:"state"`
	Author     string   `json:"author"`
	Milestone  string   `json:"milestone,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Assignees  []string `json:"assignees,omitempty"`
	Body       string   `json:"body,omitempty"`
	BodyFormat string   `json:"body_format,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

func issueToOut(i store.Issue, withBody bool) issueOut {
	o := issueOut{Number: i.Number, Title: i.Title, State: i.State, Author: i.Author,
		Milestone: i.Milestone, Labels: i.Labels, Assignees: i.Assignees, CreatedAt: i.CreatedAt}
	if withBody {
		o.Body = i.Body
		o.BodyFormat = i.BodyFormat
	}
	return o
}

func runIssueCreate(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--format", "--title", "--body", "--file"}, MaxPos: 1,
		Usage: "issue create <owner/name> --title <t> [--body <b> | --file -] [--format md|org]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, title, body, file, format := f.pos(0), f.Value("--title"), f.Value("--body"), f.Value("--file"), f.Value("--format")
	if path == "" || title == "" {
		return c.fail(protocol.ExitUsage, "usage: issue create <owner/name> --title <t> [--body <b> | --file -] [--format md|org]")
	}
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.failErr(err)
	}
	if fmtName == "" {
		fmtName = "md"
	}
	// Anyone who can read the repo can file an issue.
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	b, err := bodyFrom(c, body, file)
	if err != nil {
		return c.failErr(err)
	}
	n, err := c.Store.CreateIssue(repo.ID, c.User.ID, title, b, fmtName)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, n))
	if targets, err := c.Store.RepoNotifyTargets(repo); err == nil {
		notify(c, targets, notice{repo: repo, kind: "issue",
			subject: issueSubject(repo, n, title),
			action:  fmt.Sprintf("opened issue #%d", n),
			excerpt: b, path: fmt.Sprintf("%s/issues/%d", repo.Path(), n)})
	}
	return c.emit(Created{Number: n}, func(w io.Writer) {
		fmt.Fprintf(w, "created %s#%d\n", repo.Path(), n)
	})
}

func runIssueList(c *Ctx, args []string) int {
	args, p, code := parsePageFlags(c, args, "issue", true)
	if code >= 0 {
		return code
	}
	const usage = "usage: issue list <owner/name> [--state open|closed|all] [--label <l>] [--assignee <user>] [--author <user>] [--milestone <title>|none] [--search <text>] [--limit <n>] [--cursor <c>]"
	f := store.IssueFilter{State: "open"}
	fl, err := parseFlags(args, flagSpec{Values: []string{"--state", "--label", "--assignee", "--author", "--milestone", "--search"}, MaxPos: 1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path := fl.pos(0)
	if fl.Has("--state") {
		f.State = fl.Value("--state")
	}
	f.Label, f.Assignee, f.Author, f.Milestone = fl.Value("--label"), fl.Value("--assignee"), fl.Value("--author"), fl.Value("--milestone")
	f.Search = fl.Value("--search")
	if fl.Has("--search") {
		if err := validQuery(f.Search); err != nil {
			return c.failErr(err)
		}
	}
	if path == "" || (f.State != "open" && f.State != "closed" && f.State != "all") {
		return c.fail(protocol.ExitUsage, usage)
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	f.Limit, f.Before = p.queryLimit(), p.keyInt()
	issues, err := c.Store.QueryIssues(repo.ID, f)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	issues, next := trimPage(p, issues, "issue", func(i store.Issue) string {
		return strconv.FormatInt(i.Number, 10)
	})
	var ds []issueOut
	for _, i := range issues {
		ds = append(ds, issueToOut(i, false))
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "#%d\t%s\t%s\t%s\n", d.Number, d.State, d.Title, d.Author)
		}
	})
}

func runIssueShow(c *Ctx, args []string) int {
	repo, issue, code := issueRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: issue show <owner/name> <n>")
	}
	comments, err := c.Store.ListIssueComments(issue.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var cs []commentOut
	for _, cm := range comments {
		cs = append(cs, commentOut{cm.Author, cm.Body, cm.BodyFormat, cm.CreatedAt})
	}
	d := IssueShow{issueOut: issueToOut(issue, true), Comments: cs}
	_ = repo
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "#%d %s [%s] by %s\n", d.Number, d.Title, d.State, d.Author)
		if len(d.Labels) > 0 {
			fmt.Fprintf(w, "labels: %s\n", strings.Join(d.Labels, ", "))
		}
		if len(d.Assignees) > 0 {
			fmt.Fprintf(w, "assignees: %s\n", strings.Join(d.Assignees, ", "))
		}
		if d.Body != "" {
			fmt.Fprintf(w, "\n%s\n", d.Body)
		}
		for _, cm := range cs {
			fmt.Fprintf(w, "\n--- %s at %s\n%s\n", cm.Author, cm.CreatedAt, cm.Body)
		}
	})
}

func runIssueComment(c *Ctx, args []string) int {
	return runComment(c, args, issueThread, "issue",
		func(rest []string) (store.Repo, int64, int64, string, int) {
			repo, issue, code := issueRef(c, rest, policy.CanRead)
			return repo, issue.ID, issue.Number, issue.Title, code
		},
		c.Store.AddIssueComment, c.Store.IssueParticipants)
}

func setIssueState(c *Ctx, args []string, state string) int {
	// Author may close/reopen their own issue; otherwise write access.
	repo, issue, code := issueRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: issue %s <owner/name> <n>", state)
	}
	if code := authorOrWrite(c, repo, issue.Author, map[string]string{"open": "reopen", "closed": "close"}[state]+" this issue"); code >= 0 {
		return code
	}
	if issue.State == state {
		return c.fail(protocol.ExitUsage, "issue #%d is already %s", issue.Number, state)
	}
	if err := c.Store.SetIssueState(issue.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue."+state, fmt.Sprintf(`{"number":%d}`, issue.Number))
	if parts, err := c.Store.IssueParticipants(issue.ID); err == nil {
		verb := map[string]string{"open": "reopened", "closed": "closed"}[state]
		notify(c, parts, notice{repo: repo, kind: "issue",
			subject: issueSubject(repo, issue.Number, issue.Title),
			action:  fmt.Sprintf("%s #%d", verb, issue.Number),
			path:    fmt.Sprintf("%s/issues/%d", repo.Path(), issue.Number)})
	}
	return c.emit(map[string]any{"number": issue.Number, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s#%d is now %s\n", repo.Path(), issue.Number, state)
	})
}

// editText parses --title/--body/--file -/--format and authorizes: author or
// write. A nil format means the stored markup format stays as it is.
func editText(c *Ctx, args []string, kind string) (rest []string, title, body, format *string, code int) {
	f, err := parseFlags(args, flagSpec{Values: []string{"--title", "--body", "--file", "--format"}, MaxPos: -1,
		Usage: kind + " edit <owner/name> <n> [--title <t>] [--body <b> | --file -] [--format md|org]"})
	if err != nil {
		return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "%v", err)
	}
	rest = f.Pos
	titleV, bodyV, file, formatV := f.Value("--title"), f.Value("--body"), f.Value("--file"), f.Value("--format")
	haveTitle, haveBody := f.Has("--title"), f.Has("--body")
	if file != "" {
		b, err := bodyFrom(c, "", file)
		if err != nil {
			return nil, nil, nil, nil, c.failErr(err)
		}
		bodyV, haveBody = b, true
	}
	fmtName, err := markupFormat(formatV)
	if err != nil {
		return nil, nil, nil, nil, c.failErr(err)
	}
	if !haveTitle && !haveBody && fmtName == "" {
		return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "usage: %s edit <owner/name> <n> [--title <t>] [--body <b> | --file -] [--format md|org]", kind)
	}
	if haveTitle {
		if strings.TrimSpace(titleV) == "" {
			return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "--title must not be empty")
		}
		title = &titleV
	}
	if haveBody {
		body = &bodyV
	}
	if fmtName != "" {
		format = &fmtName
	}
	return rest, title, body, format, -1
}

func runIssueEdit(c *Ctx, args []string) int {
	rest, title, body, format, code := editText(c, args, "issue")
	if code >= 0 {
		return code
	}
	repo, issue, code := issueRef(c, rest, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if code := authorOrWrite(c, repo, issue.Author, "edit this issue"); code >= 0 {
		return code
	}
	if err := c.Store.UpdateIssueText(issue.ID, title, body, format); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": issue.Number}, func(w io.Writer) {
		fmt.Fprintf(w, "edited %s#%d\n", repo.Path(), issue.Number)
	})
}

func runIssueClose(c *Ctx, args []string) int  { return setIssueState(c, args, "closed") }
func runIssueReopen(c *Ctx, args []string) int { return setIssueState(c, args, "open") }

// addRemoveFlags parses repeated --add/--remove flags.
func addRemoveFlags(args []string) (rest, adds, removes []string, err error) {
	f, err := parseFlags(args, flagSpec{Multi: []string{"--add", "--remove"}, MaxPos: -1})
	if err != nil {
		return nil, nil, nil, err
	}
	rest, adds, removes = f.Pos, f.List("--add"), f.List("--remove")
	return rest, adds, removes, nil
}

func runIssueLabel(c *Ctx, args []string) int {
	rest, adds, removes, err := addRemoveFlags(args)
	if err != nil {
		return c.failErr(err)
	}
	if len(adds)+len(removes) == 0 {
		return c.fail(protocol.ExitUsage, "usage: issue label <owner/name> <n> [--add <l>]... [--remove <l>]...")
	}
	repo, issue, code := issueRef(c, rest, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	for _, l := range adds {
		if err := c.Store.SetIssueLabel(repo.ID, issue.ID, l, true); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	for _, l := range removes {
		if err := c.Store.SetIssueLabel(repo.ID, issue.ID, l, false); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return c.fail(protocol.ExitNotFound, "%v", err)
			}
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	updated, err := c.Store.IssueByNumber(repo.ID, issue.Number)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": issue.Number, "labels": updated.Labels}, func(w io.Writer) {
		fmt.Fprintf(w, "labels on %s#%d: %s\n", repo.Path(), issue.Number, strings.Join(updated.Labels, ", "))
	})
}

func runIssueAssign(c *Ctx, args []string) int {
	rest, adds, removes, err := addRemoveFlags(args)
	if err != nil {
		return c.failErr(err)
	}
	if len(adds)+len(removes) == 0 {
		return c.fail(protocol.ExitUsage, "usage: issue assign <owner/name> <n> [--add <user>]... [--remove <user>]...")
	}
	repo, issue, code := issueRef(c, rest, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	resolve := func(name string) (store.User, int) {
		u, err := c.Store.UserByUsername(name)
		if errors.Is(err, store.ErrNotFound) {
			return u, c.fail(protocol.ExitNotFound, "no such user %q", name)
		}
		if err != nil {
			return u, c.fail(protocol.ExitFailure, "%v", err)
		}
		return u, -1
	}
	for _, name := range adds {
		u, code := resolve(name)
		if code >= 0 {
			return code
		}
		if err := c.Store.SetIssueAssignee(issue.ID, u.ID, true); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	for _, name := range removes {
		u, code := resolve(name)
		if code >= 0 {
			return code
		}
		if err := c.Store.SetIssueAssignee(issue.ID, u.ID, false); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return c.fail(protocol.ExitNotFound, "%s is not assigned", name)
			}
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	updated, err := c.Store.IssueByNumber(repo.ID, issue.Number)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": issue.Number, "assignees": updated.Assignees}, func(w io.Writer) {
		fmt.Fprintf(w, "assignees on %s#%d: %s\n", repo.Path(), issue.Number, strings.Join(updated.Assignees, ", "))
	})
}
