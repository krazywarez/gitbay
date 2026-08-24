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
)

func init() {
	register(Command{Path: []string{"mr", "diff-comment"},
		Summary:    "comment on a diff line: mr diff-comment <owner/name> <n> --path <file> --line <l> [--old] [--reply <id>] [--message <m> | --file -]",
		ReadsStdin: true, Run: runDiffComment})
	register(Command{Path: []string{"mr", "threads"},
		Summary: "review threads on an MR: mr threads <owner/name> <n>", ReadOnly: true, Run: runMRThreads})
	register(Command{Path: []string{"mr", "resolve"},
		Summary: "resolve a review thread: mr resolve <owner/name> <n> <thread-id>", Run: runMRResolve})
	register(Command{Path: []string{"mr", "unresolve"},
		Summary: "reopen a review thread: mr unresolve <owner/name> <n> <thread-id>", Run: runMRUnresolve})
}

func runDiffComment(c *Ctx, args []string) int {
	var rest []string
	var path, message, file string
	var line, replyTo int64
	old := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--path", "--line", "--reply", "--message", "--file":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			v := args[i+1]
			switch args[i] {
			case "--path":
				path = v
			case "--line":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 1 {
					return c.fail(protocol.ExitUsage, "--line must be a positive number")
				}
				line = n
			case "--reply":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 1 {
					return c.fail(protocol.ExitUsage, "--reply must be a thread id")
				}
				replyTo = n
			case "--message":
				message = v
			case "--file":
				file = v
			}
			i++
		case "--old":
			old = true
		default:
			rest = append(rest, args[i])
		}
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
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if strings.TrimSpace(body) == "" {
		return c.fail(protocol.ExitUsage, "empty comment; use --message or --file -")
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
	}

	id, err := c.Store.AddDiffComment(mr.ID, c.User.ID, mr.HeadSHA, path, side, line, body, replyTo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%v", err)
		}
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
		notifyUsers(c, parts, mrSubject(repo, mr.Number, mr.Title),
			notifyBody(c, fmt.Sprintf("commented on %s:%d in !%d", path, line, mr.Number), body,
				fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)))
	}
	return c.emit(map[string]any{"id": id, "thread": firstNonZero(replyTo, id)}, func(w io.Writer) {
		if replyTo != 0 {
			fmt.Fprintf(w, "replied to thread %d on %s!%d\n", replyTo, repo.Path(), mr.Number)
		} else {
			fmt.Fprintf(w, "thread %d opened on %s:%d in %s!%d\n", id, path, line, repo.Path(), mr.Number)
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
		return c.fail(protocol.ExitUsage, "usage: mr threads <owner/name> <n>")
	}
	comments, err := c.Store.ListDiffComments(mr.ID)
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
		ID       int64        `json:"id"`
		Path     string       `json:"path"`
		Side     string       `json:"side"`
		Line     int64        `json:"line"`
		Stale    bool         `json:"stale"`
		Resolved string       `json:"resolved_by,omitempty"`
		Comments []commentOut `json:"comments"`
	}
	byRoot := map[int64]*threadOut{}
	var order []int64
	for _, cm := range comments {
		if cm.ReplyTo == 0 {
			byRoot[cm.ID] = &threadOut{
				ID: cm.ID, Path: cm.Path, Side: cm.Side, Line: cm.Line,
				Stale: cm.HeadSHA != mr.HeadSHA, Resolved: cm.ResolvedBy,
				Comments: []commentOut{{cm.ID, cm.Author, cm.Body, cm.CreatedAt}},
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
	_ = repo
	return c.emit(ds, func(w io.Writer) {
		for _, th := range ds {
			marks := ""
			if th.Resolved != "" {
				marks += " [resolved by " + th.Resolved + "]"
			}
			if th.Stale {
				marks += " [stale]"
			}
			fmt.Fprintf(w, "thread %d  %s:%d (%s)%s\n", th.ID, th.Path, th.Line, th.Side, marks)
			for _, cm := range th.Comments {
				fmt.Fprintf(w, "  %s: %s\n", cm.Author, cm.Body)
			}
		}
	})
}

func setThreadResolved(c *Ctx, args []string, resolved bool) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: mr resolve|unresolve <owner/name> <n> <thread-id>")
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
