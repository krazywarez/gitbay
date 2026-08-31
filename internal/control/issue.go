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
		Usage:   "issue list <owner/name> [--state open|closed|all] [--limit <n>] [--cursor <c>]", ReadOnly: true, Run: runIssueList})
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
	var path, title, body, file, format string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--format requires a value")
			}
			format = args[i+1]
			i++
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
		return c.fail(protocol.ExitUsage, "usage: issue create <owner/name> --title <t> [--body <b> | --file -] [--format md|org]")
	}
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
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
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	n, err := c.Store.CreateIssue(repo.ID, c.User.ID, title, b, fmtName)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, n))
	if targets, err := c.Store.RepoNotifyTargets(repo); err == nil {
		notifyUsers(c, targets, issueSubject(repo, n, title),
			notifyBody(c, fmt.Sprintf("opened issue #%d", n), b, fmt.Sprintf("%s/issues/%d", repo.Path(), n)))
	}
	return c.emit(map[string]any{"number": n}, func(w io.Writer) {
		fmt.Fprintf(w, "created %s#%d\n", repo.Path(), n)
	})
}

func runIssueList(c *Ctx, args []string) int {
	args, p, code := parsePageFlags(c, args, "issue", true)
	if code >= 0 {
		return code
	}
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
		return c.fail(protocol.ExitUsage, "usage: issue list <owner/name> [--state open|closed|all] [--limit <n>] [--cursor <c>]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	issues, err := c.Store.ListIssues(repo.ID, state, p.queryLimit(), p.keyInt())
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
	type commentOut struct {
		Author     string `json:"author"`
		Body       string `json:"body"`
		BodyFormat string `json:"body_format,omitempty"`
		CreatedAt  string `json:"created_at"`
	}
	var cs []commentOut
	for _, cm := range comments {
		cs = append(cs, commentOut{cm.Author, cm.Body, cm.BodyFormat, cm.CreatedAt})
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
	var message, file, format string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--format requires a value")
			}
			format = args[i+1]
			i++
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
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if fmtName == "" {
		fmtName = "md"
	}
	repo, issue, code := issueRef(c, rest, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	body, err := bodyFrom(c, message, file)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if strings.TrimSpace(body) == "" {
		return c.fail(protocol.ExitUsage, "empty comment; use --message or --file -")
	}
	if err := c.Store.AddIssueComment(issue.ID, c.User.ID, body, fmtName); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.commented", fmt.Sprintf(`{"number":%d}`, issue.Number))
	if parts, err := c.Store.IssueParticipants(issue.ID); err == nil {
		notifyUsers(c, parts, issueSubject(repo, issue.Number, issue.Title),
			notifyBody(c, fmt.Sprintf("commented on #%d", issue.Number), body, fmt.Sprintf("%s/issues/%d", repo.Path(), issue.Number)))
	}
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
	if code := refuseArchived(c, repo); code >= 0 {
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
	if parts, err := c.Store.IssueParticipants(issue.ID); err == nil {
		verb := map[string]string{"open": "reopened", "closed": "closed"}[state]
		notifyUsers(c, parts, issueSubject(repo, issue.Number, issue.Title),
			notifyBody(c, fmt.Sprintf("%s #%d", verb, issue.Number), "", fmt.Sprintf("%s/issues/%d", repo.Path(), issue.Number)))
	}
	return c.emit(map[string]any{"number": issue.Number, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s#%d is now %s\n", repo.Path(), issue.Number, state)
	})
}

// editText parses --title/--body/--file -/--format and authorizes: author or
// write. A nil format means the stored markup format stays as it is.
func editText(c *Ctx, args []string, kind string) (rest []string, title, body, format *string, code int) {
	var titleV, bodyV, file, formatV string
	haveTitle, haveBody := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title", "--body", "--file", "--format":
			if i+1 >= len(args) {
				return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			switch args[i] {
			case "--title":
				titleV, haveTitle = args[i+1], true
			case "--body":
				bodyV, haveBody = args[i+1], true
			case "--file":
				file = args[i+1]
			case "--format":
				formatV = args[i+1]
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if file != "" {
		b, err := bodyFrom(c, "", file)
		if err != nil {
			return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "%v", err)
		}
		bodyV, haveBody = b, true
	}
	fmtName, err := markupFormat(formatV)
	if err != nil {
		return nil, nil, nil, nil, c.fail(protocol.ExitUsage, "%v", err)
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
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if issue.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can edit this issue")
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
		return c.fail(protocol.ExitUsage, "%v", err)
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
