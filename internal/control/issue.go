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
		Summary:    "open an issue: issue create <owner/name> --title <t> [--body <b> | --file -]",
		ReadsStdin: true, Run: runIssueCreate})
	register(Command{Path: []string{"issue", "list"},
		Summary: "list issues: issue list <owner/name> [--state open|closed|all]", ReadOnly: true, Run: runIssueList})
	register(Command{Path: []string{"issue", "show"},
		Summary: "show an issue with comments: issue show <owner/name> <n>", ReadOnly: true, Run: runIssueShow})
	register(Command{Path: []string{"issue", "comment"},
		Summary:    "comment: issue comment <owner/name> <n> [--message <m> | --file -]",
		ReadsStdin: true, Run: runIssueComment})
	register(Command{Path: []string{"issue", "close"},
		Summary: "close an issue: issue close <owner/name> <n>", Run: runIssueClose})
	register(Command{Path: []string{"issue", "reopen"},
		Summary: "reopen an issue: issue reopen <owner/name> <n>", Run: runIssueReopen})
	register(Command{Path: []string{"issue", "label"},
		Summary: "labels: issue label <owner/name> <n> [--add <l>]... [--remove <l>]...", Run: runIssueLabel})
	register(Command{Path: []string{"issue", "assign"},
		Summary: "assignees: issue assign <owner/name> <n> [--add <user>]... [--remove <user>]...", Run: runIssueAssign})
}

// issueArgs parses "<owner/name> <n>" plus flags handled by the caller.
func issueRef(c *Ctx, args []string, perm func(store.User, store.Repo, string) bool) (store.Repo, store.Issue, int) {
	if len(args) < 2 {
		return store.Repo{}, store.Issue{}, c.fail(protocol.ExitUsage, "expected <owner/name> <number>")
	}
	repo, code := resolveRepo(c, args[0], perm)
	if code >= 0 {
		return repo, store.Issue{}, code
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return repo, store.Issue{}, c.fail(protocol.ExitUsage, "bad issue number %q", args[1])
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

type issueOut struct {
	Number    int64    `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Author    string   `json:"author"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Body      string   `json:"body,omitempty"`
	CreatedAt string   `json:"created_at"`
}

func issueToOut(i store.Issue, withBody bool) issueOut {
	o := issueOut{Number: i.Number, Title: i.Title, State: i.State, Author: i.Author,
		Labels: i.Labels, Assignees: i.Assignees, CreatedAt: i.CreatedAt}
	if withBody {
		o.Body = i.Body
	}
	return o
}

func runIssueCreate(c *Ctx, args []string) int {
	var path, title, body, file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--title requires a value")
			}
			title = args[i+1]
			i++
		case "--body":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--body requires a value")
			}
			body = args[i+1]
			i++
		case "--file":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--file requires a value")
			}
			file = args[i+1]
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" || title == "" {
		return c.fail(protocol.ExitUsage, "usage: issue create <owner/name> --title <t> [--body <b> | --file -]")
	}
	// Anyone who can read the repo can file an issue.
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	b, err := bodyFrom(c, body, file)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	n, err := c.Store.CreateIssue(repo.ID, c.User.ID, title, b)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, n))
	return c.emit(map[string]any{"number": n}, func(w io.Writer) {
		fmt.Fprintf(w, "created %s#%d\n", repo.Path(), n)
	})
}

func runIssueList(c *Ctx, args []string) int {
	state := "open"
	var path string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--state":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--state requires open|closed|all")
			}
			state = args[i+1]
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" || (state != "open" && state != "closed" && state != "all") {
		return c.fail(protocol.ExitUsage, "usage: issue list <owner/name> [--state open|closed|all]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	issues, err := c.Store.ListIssues(repo.ID, state)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var ds []issueOut
	for _, i := range issues {
		ds = append(ds, issueToOut(i, false))
	}
	return c.emit(ds, func(w io.Writer) {
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
	type commentOut struct {
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	var cs []commentOut
	for _, cm := range comments {
		cs = append(cs, commentOut{cm.Author, cm.Body, cm.CreatedAt})
	}
	d := struct {
		issueOut
		Comments []commentOut `json:"comments,omitempty"`
	}{issueToOut(issue, true), cs}
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
	var rest []string
	var message, file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--message requires a value")
			}
			message = args[i+1]
			i++
		case "--file":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--file requires a value")
			}
			file = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	repo, issue, code := issueRef(c, rest, policy.CanRead)
	if code >= 0 {
		return code
	}
	body, err := bodyFrom(c, message, file)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if strings.TrimSpace(body) == "" {
		return c.fail(protocol.ExitUsage, "empty comment; use --message or --file -")
	}
	if err := c.Store.AddIssueComment(issue.ID, c.User.ID, body); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.commented", fmt.Sprintf(`{"number":%d}`, issue.Number))
	return c.emit(map[string]any{"number": issue.Number}, func(w io.Writer) {
		fmt.Fprintf(w, "commented on %s#%d\n", repo.Path(), issue.Number)
	})
}

func setIssueState(c *Ctx, args []string, state string) int {
	// Author may close/reopen their own issue; otherwise write access.
	repo, issue, code := issueRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: issue %s <owner/name> <n>", state)
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if issue.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can %s this issue",
			map[string]string{"open": "reopen", "closed": "close"}[state])
	}
	if issue.State == state {
		return c.fail(protocol.ExitUsage, "issue #%d is already %s", issue.Number, state)
	}
	if err := c.Store.SetIssueState(issue.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue."+state, fmt.Sprintf(`{"number":%d}`, issue.Number))
	return c.emit(map[string]any{"number": issue.Number, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s#%d is now %s\n", repo.Path(), issue.Number, state)
	})
}

func runIssueClose(c *Ctx, args []string) int  { return setIssueState(c, args, "closed") }
func runIssueReopen(c *Ctx, args []string) int { return setIssueState(c, args, "open") }

// addRemoveFlags parses repeated --add/--remove flags.
func addRemoveFlags(args []string) (rest, adds, removes []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--add":
			if i+1 >= len(args) {
				return nil, nil, nil, errors.New("--add requires a value")
			}
			adds = append(adds, args[i+1])
			i++
		case "--remove":
			if i+1 >= len(args) {
				return nil, nil, nil, errors.New("--remove requires a value")
			}
			removes = append(removes, args[i+1])
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, adds, removes, nil
}

func runIssueLabel(c *Ctx, args []string) int {
	rest, adds, removes, err := addRemoveFlags(args)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(adds)+len(removes) == 0 {
		return c.fail(protocol.ExitUsage, "usage: issue label <owner/name> <n> [--add <l>]... [--remove <l>]...")
	}
	repo, issue, code := issueRef(c, rest, policy.CanWrite)
	if code >= 0 {
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
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(adds)+len(removes) == 0 {
		return c.fail(protocol.ExitUsage, "usage: issue assign <owner/name> <n> [--add <user>]... [--remove <user>]...")
	}
	repo, issue, code := issueRef(c, rest, policy.CanWrite)
	if code >= 0 {
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
