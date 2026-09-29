package control

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/suggest"
)

func init() {
	register(Command{Path: []string{"mr", "diff-comment"},
		Summary: "comment on a diff line",
		Usage:   "mr diff-comment <owner/name> <n> --path <file> --line <l> [--start-line <s>] [--old] [--pending] [--reply <id>] [--message <m> | --file -]",
		Flags: []Flag{
			{"--path", "<file>", "the file the comment is on", ""},
			{"--line", "<l>", "the line the comment is on, or the last line of a range", ""},
			{"--start-line", "<s>", "the first line of a range ending at --line", ""},
			{"--old", "", "the line is on the old side of the diff", ""},
			{"--pending", "", "hold the comment for `mr review --comment`", ""},
			{"--reply", "<id>", "reply to this thread instead of opening one", ""},
			{"--message", "<m>", "the comment's text", ""},
			{"--file", "-", "read the comment from stdin", ""},
		},
		Examples: []string{
			`mr diff-comment krz/gitbay 431 --path internal/control/build.go --line 42 --message "why is this a switch"`,
			"mr diff-comment krz/gitbay 431 --reply 12 --file - < notes.md",
			"mr diff-comment krz/gitbay 431 --path go.mod --start-line 3 --line 4 --file - < suggestion.md",
		},
		ReadsStdin: true, Run: runDiffComment})
	register(Command{Path: []string{"mr", "threads"},
		Summary:  "review threads on an MR",
		Usage:    "mr threads <owner/name> <n>",
		Examples: []string{"mr threads krz/gitbay 431"},
		ReadOnly: true, Run: runMRThreads})
	register(Command{Path: []string{"mr", "resolve"},
		Summary:  "resolve a review thread",
		Usage:    "mr resolve <owner/name> <n> <thread-id>",
		Examples: []string{"mr resolve krz/gitbay 431 12"},
		Run:      runMRResolve})
	register(Command{Path: []string{"mr", "unresolve"},
		Summary:  "reopen a review thread",
		Usage:    "mr unresolve <owner/name> <n> <thread-id>",
		Examples: []string{"mr unresolve krz/gitbay 431 12"},
		Run:      runMRUnresolve})
}

func runDiffComment(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--path", "--line", "--start-line", "--reply", "--message", "--file"},
		Bools: []string{"--old", "--pending"}, MaxPos: -1,
		Usage: "mr diff-comment <owner/name> <n> --path <file> --line <l> [--start-line <s>] [--old] [--pending] [--reply <id>] [--message <m> | --file -]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	rest := f.Pos
	path, message, file, old := f.Value("--path"), f.Value("--message"), f.Value("--file"), f.Has("--old")
	var line, startLine, replyTo int64
	if f.Has("--line") {
		n, err := strconv.ParseInt(f.Value("--line"), 10, 64)
		if err != nil || n < 1 {
			return c.fail(protocol.ExitUsage, "--line must be a positive number")
		}
		line = n
	}
	if f.Has("--start-line") {
		n, err := strconv.ParseInt(f.Value("--start-line"), 10, 64)
		if err != nil || n < 1 || (line != 0 && n > line) {
			return c.fail(protocol.ExitUsage, "--start-line must be a positive number no greater than --line")
		}
		startLine = n
	}
	if f.Has("--reply") {
		n, err := strconv.ParseInt(f.Value("--reply"), 10, 64)
		if err != nil || n < 1 {
			return c.fail(protocol.ExitUsage, "--reply must be a thread id")
		}
		replyTo = n
	}
	repo, mr, code := mrRef(c, rest, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if replyTo == 0 && (path == "" || line == 0) {
		return c.fail(protocol.ExitUsage, "a new thread needs --path and --line (or reply to one with --reply <id>)")
	}
	body, err := bodyFrom(c, message, file)
	if err != nil {
		return c.failInput(err)
	}
	if strings.TrimSpace(body) == "" {
		return c.fail(protocol.ExitUsage, "empty comment; use --message or --file -")
	}
	if replyTo != 0 && startLine != 0 {
		return c.fail(protocol.ExitUsage, "a reply takes its thread's lines; drop --start-line")
	}
	_, hasSuggestion, err := suggest.Parse(body)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if hasSuggestion && replyTo != 0 {
		return c.fail(protocol.ExitUsage, "a suggestion opens its own thread: post it with --path and --line, not --reply")
	}
	if hasSuggestion && old {
		return c.fail(protocol.ExitUsage, "a suggestion replaces lines of the new file; drop --old")
	}

	side := "new"
	if old {
		side = "old"
	}
	if replyTo == 0 {
		// The path must actually be part of the MR's diff.
		dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
		base := mr.MergedBase
		if base == "" {
			b, err := gitutil.MergeBase(dir, "refs/heads/"+mr.TargetRef, mrHeadRef(mr.Number))
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			base = b
		}
		files, err := gitutil.DiffFiles(dir, base, mrHeadRef(mr.Number))
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !slices.Contains(files, path) {
			return c.fail(protocol.ExitUsage, "%s is not part of this merge request's diff", path)
		}
		if hasSuggestion {
			first := firstNonZero(startLine, line)
			content, _, err := readAnchored(dir, mr.HeadSHA, path)
			if err != nil {
				return c.fail(protocol.ExitUsage, "%v", err)
			}
			if _, ok := suggest.Range(content, int(first), int(line)); !ok {
				return c.fail(protocol.ExitUsage, "%s has no lines %d-%d at the head", path, first, line)
			}
		}
	}

	pending := f.Has("--pending")
	id, err := c.Store.AddDiffComment(mr.ID, c.User.ID, mr.HeadSHA, path, side, line, startLine, body, replyTo, pending)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%v", err)
		}
		return c.failErr(err)
	}
	// A pending comment is not part of the conversation yet, so it does
	// not reach anyone's inbox. `mr review` is what says it out loud.
	if !pending {
		if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
			notify(c, parts, notice{repo: repo, kind: "mr",
				subject: mrSubject(repo, mr.Number, mr.Title),
				action:  fmt.Sprintf("commented on %s:%d in !%d", path, line, mr.Number),
				excerpt: body, path: fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)})
		}
		notifyMentions(c, repo, mrThread, mr.ID, mr.Number, mr.Title, body)
	}
	return c.emit(map[string]any{"id": id, "thread": firstNonZero(replyTo, id), "pending": pending}, func(w io.Writer) {
		what := "thread %d opened on %s:%d in %s!%d\n"
		if replyTo != 0 {
			fmt.Fprintf(w, "replied to thread %d on %s!%d\n", replyTo, repo.Path(), mr.Number)
		} else {
			fmt.Fprintf(w, what, id, path, line, repo.Path(), mr.Number)
		}
		if pending {
			n := c.Store.CountPendingComments(mr.ID, c.User.ID)
			fmt.Fprintf(w, "pending: %d comment(s) in this review, submit with `gitbay mr review %s %d --comment`\n",
				n, repo.Path(), mr.Number)
		}
	})
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func runMRThreads(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.usage()
	}
	comments, err := c.Store.ListDiffComments(mr.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type commentOut struct {
		ID        int64  `json:"id"`
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	type threadOut struct {
		ID         int64          `json:"id"`
		Path       string         `json:"path"`
		Side       string         `json:"side"`
		StartLine  int64          `json:"start_line,omitempty"`
		Line       int64          `json:"line"`
		Stale      bool           `json:"stale"`
		Resolved   string         `json:"resolved_by,omitempty"`
		Suggestion *SuggestionOut `json:"suggestion,omitempty"`
		Comments   []commentOut   `json:"comments"`
	}
	suggestions := Suggestions(c.Store, c.Cfg.Server.Root, repo, mr, comments)
	byRoot := map[int64]*threadOut{}
	var order []int64
	for _, cm := range comments {
		if cm.ReplyTo == 0 {
			byRoot[cm.ID] = &threadOut{
				ID: cm.ID, Path: cm.Path, Side: cm.Side, StartLine: cm.StartLine, Line: cm.Line,
				Stale: cm.HeadSHA != mr.HeadSHA, Resolved: cm.ResolvedBy,
				Suggestion: suggestions[cm.ID],
				Comments:   []commentOut{{cm.ID, cm.Author, cm.Body, cm.CreatedAt}},
			}
			order = append(order, cm.ID)
		} else if th, ok := byRoot[cm.ReplyTo]; ok {
			th.Comments = append(th.Comments, commentOut{cm.ID, cm.Author, cm.Body, cm.CreatedAt})
		}
	}
	var ds []threadOut
	for _, id := range order {
		ds = append(ds, *byRoot[id])
	}
	return c.emit(ds, func(w io.Writer) {
		for _, th := range ds {
			marks := ""
			if th.Resolved != "" {
				marks += " [resolved by " + th.Resolved + "]"
			}
			if th.Stale {
				marks += " [stale]"
			}
			lines := fmt.Sprint(th.Line)
			if th.StartLine != 0 && th.StartLine != th.Line {
				lines = fmt.Sprintf("%d-%d", th.StartLine, th.Line)
			}
			fmt.Fprintf(w, "thread %d  %s:%s (%s)%s\n", th.ID, th.Path, lines, th.Side, marks)
			for i, cm := range th.Comments {
				body := cm.Body
				if i == 0 && th.Suggestion != nil {
					body = suggest.Strip(body)
				}
				if body != "" {
					fmt.Fprintf(w, "  %s: %s\n", cm.Author, body)
				}
				if i == 0 && th.Suggestion != nil {
					writeSuggestion(w, repo, mr, th.ID, cm.Author, th.Suggestion)
				}
			}
		}
	})
}

// writeSuggestion prints a suggestion as the diff it proposes and how to
// apply it, or why it can no longer be applied.
func writeSuggestion(w io.Writer, repo store.Repo, mr store.MR, thread int64, author string, s *SuggestionOut) {
	switch {
	case s.Outdated:
		fmt.Fprintf(w, "  %s suggests (outdated: %s):\n", author, s.Reason)
	default:
		fmt.Fprintf(w, "  %s suggests (gitbay mr apply-suggestion %s %d %d):\n", author, repo.Path(), mr.Number, thread)
	}
	for _, l := range suggest.FromText(strings.ReplaceAll(s.Original, "\r\n", "\n")) {
		fmt.Fprintf(w, "  - %s\n", l)
	}
	for _, l := range suggest.FromText(s.Replacement) {
		fmt.Fprintf(w, "  + %s\n", l)
	}
}

func setThreadResolved(c *Ctx, args []string, resolved bool) int {
	if len(args) != 3 {
		return c.usage()
	}
	repo, mr, code := mrRef(c, args[:2], policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	threadID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad thread id %q", args[2])
	}
	// Thread author, MR author, or anyone with write may resolve.
	author, err := c.Store.DiffCommentAuthor(mr.ID, threadID)
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no thread %d on %s!%d", threadID, repo.Path(), mr.Number)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if author != c.User.ID && mr.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the thread author, the MR author, or users with write access can resolve threads")
	}
	if err := c.Store.SetThreadResolved(mr.ID, threadID, c.User.ID, resolved); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no thread %d (replies cannot be resolved; use the root id)", threadID)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if resolved {
		TryQueuedMerge(c.Store, c.Cfg, mr.ID)
	}
	verb := "resolved"
	if !resolved {
		verb = "reopened"
	}
	return c.emit(map[string]any{"thread": threadID, "resolved": resolved}, func(w io.Writer) {
		fmt.Fprintf(w, "%s thread %d on %s!%d\n", verb, threadID, repo.Path(), mr.Number)
	})
}

func runMRResolve(c *Ctx, args []string) int   { return setThreadResolved(c, args, true) }
func runMRUnresolve(c *Ctx, args []string) int { return setThreadResolved(c, args, false) }
