package control

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/sig"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"repo", "fork"},
		Summary: "fork a repository under your account",
		Usage:   "repo fork <owner/name> [--name <n>]", Run: runRepoFork})
	register(Command{Path: []string{"repo", "settings", "require-approvals"},
		Summary: "require N fresh approvals to merge",
		Usage:   "repo settings require-approvals <owner/name> <n> (0 = off)", Run: runRequireApprovals})
	register(Command{Path: []string{"repo", "settings", "require-resolved"},
		Summary: "require all review threads resolved to merge",
		Usage:   "repo settings require-resolved <owner/name> on|off", Run: runRequireResolved})
	register(Command{Path: []string{"repo", "settings", "require-checks"},
		Summary: "gate merges on green statuses",
		Usage:   "repo settings require-checks <owner/name> on|off", Run: runRequireChecks})
	register(Command{Path: []string{"repo", "settings", "require-signed"},
		Summary: "require verified commit signatures",
		Usage:   "repo settings require-signed <owner/name> on|off", Run: runRequireSigned})
	register(Command{Path: []string{"mr", "create"},
		Summary:    "open a merge request",
		Usage:      "mr create <target owner/name> --source [owner/name:]<branch> --target <branch> --title <t> [--body <b> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runMRCreate})
	register(Command{Path: []string{"mr", "list"},
		Summary: "list merge requests",
		Usage:   "mr list <owner/name> [--state open|merged|closed|source_gone|all] [--limit <n>] [--cursor <c>]", ReadOnly: true, Run: runMRList})
	register(Command{Path: []string{"mr", "show"},
		Summary: "show a merge request",
		Usage:   "mr show <owner/name> <n>", ReadOnly: true, Run: runMRShow})
	register(Command{Path: []string{"mr", "diff"},
		Summary: "show the diff",
		Usage:   "mr diff <owner/name> <n>", ReadOnly: true, Run: runMRDiff})
	register(Command{Path: []string{"mr", "edit"},
		Summary:    "edit title or body",
		Usage:      "mr edit <owner/name> <n> [--title <t>] [--body <b> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runMREdit})
	register(Command{Path: []string{"mr", "retarget"},
		Summary: "retarget onto another branch",
		Usage:   "mr retarget <owner/name> <n> <branch>", Run: runMRRetarget})
	register(Command{Path: []string{"mr", "comment"},
		Summary:    "comment",
		Usage:      "mr comment <owner/name> <n> [--message <m> | --file -] [--format md|org]",
		ReadsStdin: true, Run: runMRComment})
	register(Command{Path: []string{"mr", "review"},
		Summary: "review",
		Usage:   "mr review <owner/name> <n> --approve|--request-changes|--comment", Run: runMRReview})
	register(Command{Path: []string{"mr", "merge"},
		Summary: "merge",
		Usage:   "mr merge <owner/name> <n> [--strategy ff|merge|squash|rebase]", Run: runMRMerge})
	register(Command{Path: []string{"mr", "close"},
		Summary: "close without merging",
		Usage:   "mr close <owner/name> <n>", Run: runMRClose})
}

func runRepoFork(c *Ctx, args []string) int {
	var path, name string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--name requires a value")
			}
			name = args[i+1]
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "usage: repo fork <owner/name> [--name <n>]")
			}
			path = args[i]
		}
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: repo fork <owner/name> [--name <n>]")
	}
	src, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	if name == "" {
		name = src.Name
	}
	if err := policy.ValidateName(name); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	id, err := c.Store.CreateRepo("user", c.User.ID, name, src.Visibility)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.SetForkOf(id, src.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	dstDir := RepoDir(c.Cfg.Server.Root, c.User.Username, name)
	srcDir := RepoDir(c.Cfg.Server.Root, src.OwnerName, src.Name)
	if err := gitutil.InitBare(dstDir, "main", HooksDir(c.Cfg.Server.Root)); err != nil {
		c.Store.DeleteRepo(id)
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if desc := gitutil.ReadDescription(srcDir); desc != "" {
		gitutil.WriteDescription(dstDir, desc)
	}
	if err := gitutil.FetchInto(dstDir, srcDir, "refs/heads/*", "refs/heads/*"); err != nil {
		// Empty source repos have nothing to fetch; that is fine.
		if _, rerr := gitutil.ResolveRef(srcDir, src.DefaultBranch); rerr == nil {
			c.Store.DeleteRepo(id)
			return c.fail(protocol.ExitFailure, "copying refs: %v", err)
		}
	}
	forkPath := c.User.Username + "/" + name
	return c.emit(map[string]string{"path": forkPath, "fork_of": src.Path()}, func(w io.Writer) {
		fmt.Fprintf(w, "forked %s to %s\n", src.Path(), forkPath)
	})
}

func runRequireApprovals(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo settings require-approvals <owner/name> <n>")
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 0 || n > 20 {
		return c.fail(protocol.ExitUsage, "approvals must be 0..20")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s := repo.Settings
	s.RequireApprovals = n
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		fmt.Fprintf(w, "require_approvals %d on %s\n", n, repo.Path())
	})
}

func runRequireResolved(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return c.fail(protocol.ExitUsage, "usage: repo settings require-resolved <owner/name> on|off")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s := repo.Settings
	s.RequireResolved = args[1] == "on"
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		fmt.Fprintf(w, "require_resolved %s on %s\n", args[1], repo.Path())
	})
}

func runRequireChecks(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return c.fail(protocol.ExitUsage, "usage: repo settings require-checks <owner/name> on|off")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s := repo.Settings
	s.RequireChecks = args[1] == "on"
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		fmt.Fprintf(w, "require_checks %s on %s\n", args[1], repo.Path())
	})
}

func runRequireSigned(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return c.fail(protocol.ExitUsage, "usage: repo settings require-signed <owner/name> on|off")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s := repo.Settings
	s.RequireSignedCommits = args[1] == "on"
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		fmt.Fprintf(w, "require_signed_commits %s on %s\n", args[1], repo.Path())
	})
}

// mrRef parses "<owner/name> <n>" and loads the MR.
func mrRef(c *Ctx, args []string, perm func(store.User, store.Repo, string) bool) (store.Repo, store.MR, int) {
	if len(args) < 2 {
		return store.Repo{}, store.MR{}, c.fail(protocol.ExitUsage, "expected <owner/name> <number>")
	}
	repo, code := resolveRepo(c, args[0], perm)
	if code >= 0 {
		return repo, store.MR{}, code
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return repo, store.MR{}, c.fail(protocol.ExitUsage, "bad MR number %q", args[1])
	}
	mr, err := c.Store.MRByNumber(repo.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		return repo, mr, c.fail(protocol.ExitNotFound, "MR !%d not found in %s", n, repo.Path())
	}
	if err != nil {
		return repo, mr, c.fail(protocol.ExitFailure, "%v", err)
	}
	return repo, mr, -1
}

func mrHeadRef(n int64) string { return fmt.Sprintf("refs/merge-requests/%d/head", n) }

func runMRCreate(c *Ctx, args []string) int {
	var path, source, target, title, body, file, format string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source", "--target", "--title", "--body", "--file", "--format":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			v := args[i+1]
			switch args[i] {
			case "--source":
				source = v
			case "--target":
				target = v
			case "--title":
				title = v
			case "--body":
				body = v
			case "--file":
				file = v
			case "--format":
				format = v
			}
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" || source == "" || title == "" {
		return c.fail(protocol.ExitUsage, "usage: mr create <target owner/name> --source [owner/name:]<branch> --target <branch> --title <t>")
	}
	fmtName, err := markupFormat(format)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if fmtName == "" {
		fmtName = "md"
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if target == "" {
		target = repo.DefaultBranch
	}

	// Source is "branch" (same repo) or "owner/name:branch" (a fork).
	srcRepo := repo
	srcBranch := source
	if sp, br, ok := strings.Cut(source, ":"); ok {
		srcBranch = br
		var scode int
		srcRepo, scode = resolveRepo(c, sp, policy.CanRead)
		if scode >= 0 {
			return scode
		}
		if srcRepo.ForkOf != repo.ID && srcRepo.ID != repo.ID {
			return c.fail(protocol.ExitUsage, "%s is not a fork of %s", srcRepo.Path(), repo.Path())
		}
	}
	srcDir := RepoDir(c.Cfg.Server.Root, srcRepo.OwnerName, srcRepo.Name)
	headSHA, err := gitutil.ResolveRef(srcDir, "refs/heads/"+srcBranch)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "branch %s not found in %s", srcBranch, srcRepo.Path())
	}
	b, err := bodyFrom(c, body, file)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	n, err := c.Store.CreateMR(repo.ID, c.User.ID, srcRepo.ID, srcBranch, target, title, b, headSHA, fmtName)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// Fetch the head into the target so the target owns the objects.
	dstDir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if err := gitutil.FetchInto(dstDir, srcDir, headSHA, mrHeadRef(n)); err != nil {
		return c.fail(protocol.ExitFailure, "recording MR head: %v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "mr.created", fmt.Sprintf(`{"number":%d}`, n))
	if targets, err := c.Store.RepoNotifyTargets(repo); err == nil {
		notifyUsers(c, targets, mrSubject(repo, n, title),
			notifyBody(c, fmt.Sprintf("opened merge request !%d (%s -> %s)", n, source, target), b, fmt.Sprintf("%s/mrs/%d", repo.Path(), n)))
	}
	return c.emit(map[string]any{"number": n, "head_sha": headSHA}, func(w io.Writer) {
		fmt.Fprintf(w, "created %s!%d (%s -> %s)\n", repo.Path(), n, source, target)
	})
}

type mrOut struct {
	Number     int64  `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Author     string `json:"author"`
	Source     string `json:"source"` // owner/name:branch, or branch, "" if gone
	TargetRef  string `json:"target_ref"`
	HeadSHA    string `json:"head_sha"`
	Body       string `json:"body,omitempty"`
	BodyFormat string `json:"body_format,omitempty"`
	Milestone  string `json:"milestone,omitempty"`
	CreatedAt  string `json:"created_at"`
	MergedAt   string `json:"merged_at,omitempty"`
	MergedBy   string `json:"merged_by,omitempty"`
	ClosedAt   string `json:"closed_at,omitempty"`
	ClosedBy   string `json:"closed_by,omitempty"`
}

func mrToOut(repo store.Repo, m store.MR, withBody bool) mrOut {
	src := ""
	if m.SourcePath != "" {
		if m.SourceRepoID == repo.ID {
			src = m.SourceRef
		} else {
			src = m.SourcePath + ":" + m.SourceRef
		}
	}
	o := mrOut{Number: m.Number, Title: m.Title, State: m.State, Author: m.Author,
		Source: src, TargetRef: m.TargetRef, HeadSHA: m.HeadSHA, Milestone: m.Milestone,
		CreatedAt: m.CreatedAt, MergedAt: m.MergedAt, MergedBy: m.MergedBy,
		ClosedAt: m.ClosedAt, ClosedBy: m.ClosedBy}
	if withBody {
		o.Body = m.Body
		o.BodyFormat = m.BodyFormat
	}
	return o
}

func runMRList(c *Ctx, args []string) int {
	args, p, code := parsePageFlags(c, args, "mr", true)
	if code >= 0 {
		return code
	}
	state := "open"
	var path string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--state":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--state requires a value")
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
	valid := map[string]bool{"open": true, "merged": true, "closed": true, "source_gone": true, "all": true}
	if path == "" || !valid[state] {
		return c.fail(protocol.ExitUsage, "usage: mr list <owner/name> [--state open|merged|closed|source_gone|all] [--limit <n>] [--cursor <c>]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	mrs, err := c.Store.ListMRs(repo.ID, state, p.queryLimit(), p.keyInt())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	mrs, next := trimPage(p, mrs, "mr", func(m store.MR) string {
		return strconv.FormatInt(m.Number, 10)
	})
	var ds []mrOut
	for _, m := range mrs {
		ds = append(ds, mrToOut(repo, m, false))
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "!%d\t%s\t%s\t%s -> %s\n", d.Number, d.State, d.Title, d.Source, d.TargetRef)
		}
	})
}

// byWhom renders " by <user>", or nothing when the actor is unknown — an
// imported merge request carries a time but no local account.
func byWhom(user string) string {
	if user == "" {
		return ""
	}
	return " by " + user
}

func runMRShow(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: mr show <owner/name> <n>")
	}
	comments, err := c.Store.ListMRComments(mr.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	reviews, err := c.Store.ListMRReviews(mr.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	statuses, combined, err := c.Store.ChecksForCommit(repo.ID, mr.HeadSHA)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	unresolved, err := c.Store.UnresolvedThreadCount(mr.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type commentOut struct {
		Author     string `json:"author"`
		Body       string `json:"body"`
		BodyFormat string `json:"body_format,omitempty"`
		CreatedAt  string `json:"created_at"`
	}
	type reviewOut struct {
		Reviewer  string `json:"reviewer"`
		Verdict   string `json:"verdict"`
		Stale     bool   `json:"stale"`
		CreatedAt string `json:"created_at"`
	}
	type checkOut struct {
		Context   string `json:"context"`
		State     string `json:"state"`
		URL       string `json:"url,omitempty"`
		UpdatedAt string `json:"updated_at"`
		Duration  string `json:"duration,omitempty"` // CI checks only, once finished
	}
	var checks []checkOut
	for _, st := range statuses {
		out := checkOut{Context: st.Context, State: st.State, URL: st.TargetURL, UpdatedAt: st.UpdatedAt}
		if st.Duration > 0 {
			out.Duration = st.Duration.String()
		}
		checks = append(checks, out)
	}
	var cs []commentOut
	for _, cm := range comments {
		cs = append(cs, commentOut{cm.Author, cm.Body, cm.BodyFormat, cm.CreatedAt})
	}
	var rs []reviewOut
	for _, r := range reviews {
		rs = append(rs, reviewOut{r.Reviewer, r.Verdict, r.Stale, r.CreatedAt})
	}
	// The commits this MR carries: base..head, the diff's range.
	type commitOut struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
	}
	var commits []commitOut
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	base := mr.MergedBase
	if base == "" {
		if b, err := gitutil.MergeBase(dir, "refs/heads/"+mr.TargetRef, mrHeadRef(mr.Number)); err == nil {
			base = b
		}
	}
	if base != "" {
		if shas, err := gitutil.RevListRange(dir, base, mrHeadRef(mr.Number)); err == nil {
			for _, sha := range shas {
				subject := ""
				if raw, err := gitutil.ReadCommit(dir, sha); err == nil {
					if parsed, err := sig.ParseCommit(raw); err == nil {
						subject = parsed.Subject
					}
				}
				commits = append(commits, commitOut{sha, subject})
			}
		}
	}
	d := struct {
		mrOut
		Checks            []checkOut   `json:"checks,omitempty"`
		Combined          string       `json:"checks_combined,omitempty"`
		UnresolvedThreads int          `json:"unresolved_threads,omitempty"`
		Commits           []commitOut  `json:"commits,omitempty"`
		Comments          []commentOut `json:"comments,omitempty"`
		Reviews           []reviewOut  `json:"reviews,omitempty"`
	}{mrToOut(repo, mr, true), checks, combined, unresolved, commits, cs, rs}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "!%d %s [%s] by %s\n%s -> %s @ %.10s\n", d.Number, d.Title, d.State, d.Author, d.Source, d.TargetRef, d.HeadSHA)
		if d.MergedAt != "" {
			fmt.Fprintf(w, "merged %s%s\n", d.MergedAt, byWhom(d.MergedBy))
		}
		if d.ClosedAt != "" {
			fmt.Fprintf(w, "closed %s%s\n", d.ClosedAt, byWhom(d.ClosedBy))
		}
		if d.Body != "" {
			fmt.Fprintf(w, "\n%s\n", d.Body)
		}
		for _, cm := range commits {
			fmt.Fprintf(w, "commit: %.10s %s\n", cm.SHA, cm.Subject)
		}
		for _, x := range checks {
			dur := ""
			if x.Duration != "" {
				dur = " in " + x.Duration
			}
			fmt.Fprintf(w, "check: %s %s at %s%s\n", x.Context, x.State, x.UpdatedAt, dur)
		}
		if d.UnresolvedThreads > 0 {
			fmt.Fprintf(w, "unresolved threads: %d\n", d.UnresolvedThreads)
		}
		for _, r := range rs {
			stale := ""
			if r.Stale {
				stale = " (stale)"
			}
			fmt.Fprintf(w, "review: %s %s%s at %s\n", r.Reviewer, r.Verdict, stale, r.CreatedAt)
		}
		for _, cm := range cs {
			fmt.Fprintf(w, "\n--- %s at %s\n%s\n", cm.Author, cm.CreatedAt, cm.Body)
		}
	})
}

func runMRDiff(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	head := mrHeadRef(mr.Number)
	// After a merge (especially fast-forward) the live merge-base equals
	// the head and the diff would vanish; use the recorded base instead.
	base := mr.MergedBase
	if base == "" {
		b, err := gitutil.MergeBase(dir, "refs/heads/"+mr.TargetRef, head)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		base = b
	}
	patch, err := gitutil.Diff(dir, base, head, 4<<20)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	fmt.Fprint(c.Stdout, patch)
	return protocol.ExitOK
}

func runMREdit(c *Ctx, args []string) int {
	rest, title, body, format, code := editText(c, args, "mr")
	if code >= 0 {
		return code
	}
	repo, mr, code := mrRef(c, rest, policy.CanRead)
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
	if mr.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can edit this merge request")
	}
	if err := c.Store.UpdateMRText(mr.ID, title, body, format); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": mr.Number}, func(w io.Writer) {
		fmt.Fprintf(w, "edited %s!%d\n", repo.Path(), mr.Number)
	})
}

// runMRRetarget moves an open merge request onto another branch of the
// same repository.
func runMRRetarget(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: mr retarget <owner/name> <n> <branch>")
	}
	repo, mr, code := mrRef(c, args[:2], policy.CanRead)
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
	if mr.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can retarget this merge request")
	}
	if mr.State == "merged" || mr.State == "closed" {
		return c.fail(protocol.ExitUsage, "!%d is %s; only an open merge request can be retargeted", mr.Number, mr.State)
	}
	target := args[2]
	if target == mr.TargetRef {
		return c.fail(protocol.ExitUsage, "!%d already targets %s", mr.Number, target)
	}
	if mr.SourceRepoID == repo.ID && target == mr.SourceRef {
		return c.fail(protocol.ExitUsage, "%s is the source branch of !%d", target, mr.Number)
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, "refs/heads/"+target); err != nil {
		return c.fail(protocol.ExitNotFound, "branch %s not found in %s", target, repo.Path())
	}
	// The diff, the commit list and the merge gates all derive their base
	// from the target on every read, so the only thing to check here is
	// that a base exists at all: without one there is nothing to show and
	// nothing to merge.
	base, err := gitutil.MergeBase(dir, "refs/heads/"+target, mrHeadRef(mr.Number))
	if err != nil || base == "" {
		return c.fail(protocol.ExitUsage, "%s shares no history with the head of !%d", target, mr.Number)
	}
	old := mr.TargetRef
	if err := c.Store.SetMRTarget(mr.ID, target); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.AddMRSystemComment(mr.ID, c.User.ID, fmt.Sprintf("retargeted from %s to %s", old, target))
	if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
		notifyUsers(c, parts, mrSubject(repo, mr.Number, mr.Title),
			notifyBody(c, fmt.Sprintf("retargeted !%d from %s to %s", mr.Number, old, target), "",
				fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)))
	}
	return c.emit(map[string]any{"number": mr.Number, "target_ref": target, "merge_base": base}, func(w io.Writer) {
		fmt.Fprintf(w, "retargeted %s!%d from %s to %s (base %.10s)\n", repo.Path(), mr.Number, old, target, base)
	})
}

func runMRComment(c *Ctx, args []string) int {
	var rest []string
	var message, file, format string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message", "--file", "--format":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			switch args[i] {
			case "--message":
				message = args[i+1]
			case "--file":
				file = args[i+1]
			case "--format":
				format = args[i+1]
			}
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
	repo, mr, code := mrRef(c, rest, policy.CanRead)
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
	if err := c.Store.AddMRComment(mr.ID, c.User.ID, body, fmtName); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "mr.commented", fmt.Sprintf(`{"number":%d}`, mr.Number))
	if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
		notifyUsers(c, parts, mrSubject(repo, mr.Number, mr.Title),
			notifyBody(c, fmt.Sprintf("commented on !%d", mr.Number), body, fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)))
	}
	return c.emit(map[string]any{"number": mr.Number}, func(w io.Writer) {
		fmt.Fprintf(w, "commented on %s!%d\n", repo.Path(), mr.Number)
	})
}

func runMRReview(c *Ctx, args []string) int {
	verdict := ""
	var rest []string
	for _, a := range args {
		switch a {
		case "--approve":
			verdict = "approve"
		case "--request-changes":
			verdict = "request_changes"
		case "--comment":
			verdict = "comment"
		default:
			rest = append(rest, a)
		}
	}
	if verdict == "" {
		return c.fail(protocol.ExitUsage, "usage: mr review <owner/name> <n> --approve|--request-changes|--comment")
	}
	repo, mr, code := mrRef(c, rest, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if mr.State != "open" {
		return c.fail(protocol.ExitUsage, "MR !%d is %s", mr.Number, mr.State)
	}
	if err := c.Store.AddMRReview(mr.ID, c.User.ID, verdict, mr.HeadSHA); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
		notifyUsers(c, parts, mrSubject(repo, mr.Number, mr.Title),
			notifyBody(c, fmt.Sprintf("reviewed !%d: %s", mr.Number, verdict), "", fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)))
	}
	return c.emit(map[string]any{"number": mr.Number, "verdict": verdict}, func(w io.Writer) {
		fmt.Fprintf(w, "reviewed %s!%d: %s\n", repo.Path(), mr.Number, verdict)
	})
}

func runMRMerge(c *Ctx, args []string) int {
	strategy := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--strategy" {
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--strategy requires ff|merge|squash|rebase")
			}
			strategy = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	valid := map[string]bool{"": true, "ff": true, "merge": true, "squash": true, "rebase": true}
	if !valid[strategy] {
		return c.fail(protocol.ExitUsage, "--strategy must be ff, merge, squash, or rebase")
	}
	repo, mr, code := mrRef(c, rest, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if mr.State != "open" && mr.State != "source_gone" {
		return c.fail(protocol.ExitUsage, "MR !%d is %s", mr.Number, mr.State)
	}

	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	targetRef := "refs/heads/" + mr.TargetRef
	targetSHA, err := gitutil.ResolveRef(dir, targetRef)
	if err != nil {
		return c.fail(protocol.ExitFailure, "target branch %s: %v", mr.TargetRef, err)
	}
	headSHA, err := gitutil.ResolveRef(dir, mrHeadRef(mr.Number))
	if err != nil {
		return c.fail(protocol.ExitFailure, "MR head ref: %v", err)
	}

	// Check gate: with require_checks, the MR head must carry statuses
	// and every one of them must be green.
	if repo.Settings.RequireChecks {
		statuses, err := c.Store.ListCommitStatuses(repo.ID, headSHA)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		switch store.CombinedStatus(statuses) {
		case "success":
		case "":
			return c.fail(protocol.ExitDenied,
				"%s requires green checks and none were reported on %.10s", repo.Path(), headSHA)
		default:
			var bad []string
			for _, st := range statuses {
				if st.State != "success" {
					bad = append(bad, st.Context+"="+st.State)
				}
			}
			return c.fail(protocol.ExitDenied,
				"%s requires green checks; %.10s has %s", repo.Path(), headSHA, strings.Join(bad, ", "))
		}
	}

	// Review gates: approvals, CODEOWNERS, resolved threads.
	if code := c.reviewGates(repo, mr, dir, targetSHA, headSHA); code >= 0 {
		return code
	}

	upToDate, err := gitutil.IsAncestor(dir, headSHA, targetSHA)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if upToDate {
		return c.fail(protocol.ExitUsage, "target already contains the MR head")
	}
	ffPossible, err := gitutil.IsAncestor(dir, targetSHA, headSHA)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	// Signature policy matrix: with require_signed_commits, only
	// fast-forward is allowed — squash, rebase-replay, and merge commits
	// are all server-created and unsigned, violating the branch's own
	// policy — and every landed commit must be verified. An explicit
	// rebase when fast-forward is already possible IS a fast-forward
	// (nothing is rewritten), so it stays legal.
	if repo.Settings.RequireSignedCommits {
		if strategy == "merge" || strategy == "squash" || !ffPossible {
			return c.fail(protocol.ExitDenied,
				"%s requires signed commits, so only fast-forward merges are allowed; rebase %s onto %s locally, re-push, and merge again",
				repo.Path(), mr.SourceRef, mr.TargetRef)
		}
		strategy = "ff"
		commits, err := gitutil.RevListRange(dir, targetSHA, headSHA)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, sha := range commits {
			raw, err := gitutil.ReadCommit(dir, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			parsed, err := sigParse(raw)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			res, err := VerifyCommitCached(c.Store, repo, parsed, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if res.State != "verified" {
				return c.fail(protocol.ExitDenied,
					"%s requires signed commits: %.10s is %s", repo.Path(), sha, res.State)
			}
		}
	}
	if strategy == "" {
		if ffPossible {
			strategy = "ff"
		} else {
			strategy = "merge"
		}
	}
	if strategy == "rebase" && ffPossible {
		// Nothing to rewrite: a rebase onto an ancestor is a fast-forward,
		// and taking it keeps the original commits and their signatures.
		strategy = "ff"
	}

	// Every server-created commit needs the merger's verified identity.
	mergerEmail := ""
	if strategy != "ff" {
		email, err := c.Store.PrimaryVerifiedEmail(c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if email == "" {
			return c.fail(protocol.ExitDenied,
				"%s merges create commits carrying your identity: verify a primary email first (or use a fast-forward merge)", strategy)
		}
		mergerEmail = email
	}

	var newSHA string
	switch strategy {
	case "ff":
		if !ffPossible {
			return c.fail(protocol.ExitUsage,
				"fast-forward not possible: %s has diverged from the MR head; use --strategy merge or rebase and re-push", mr.TargetRef)
		}
		newSHA = headSHA

	case "merge":
		tree, conflict, err := gitutil.MergeTree(dir, targetSHA, headSHA)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if conflict {
			return c.fail(protocol.ExitUsage,
				"merge conflicts between %s and the MR head; resolve locally and re-push", mr.TargetRef)
		}
		msg := fmt.Sprintf("Merge request !%d: %s\n\nMerged %s into %s", mr.Number, mr.Title, mr.SourceRef, mr.TargetRef)
		newSHA, err = gitutil.CommitTree(dir, tree, []string{targetSHA, headSHA}, c.User.Username, mergerEmail, msg)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}

	case "squash":
		// One new commit with the merged tree. Authorship credit goes to
		// the MR author (their verified identity when they have one); the
		// committer is the merger.
		tree := ""
		if ffPossible {
			t, err := gitutil.ResolveTree(dir, headSHA)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			tree = t
		} else {
			t, conflict, err := gitutil.MergeTree(dir, targetSHA, headSHA)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if conflict {
				return c.fail(protocol.ExitUsage,
					"merge conflicts between %s and the MR head; resolve locally and re-push", mr.TargetRef)
			}
			tree = t
		}
		authorName, authorEmail := c.User.Username, mergerEmail
		if author, err := c.Store.UserByUsername(mr.Author); err == nil {
			if ae, err := c.Store.PrimaryVerifiedEmail(author.ID); err == nil && ae != "" {
				authorName, authorEmail = author.Username, ae
			}
		}
		msg := fmt.Sprintf("%s (!%d)", mr.Title, mr.Number)
		if mr.Body != "" {
			msg += "\n\n" + mr.Body
		}
		var err error
		newSHA, err = gitutil.CommitTreeIdent(dir, tree, []string{targetSHA},
			authorName, authorEmail, "", c.User.Username, mergerEmail, msg)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}

	case "rebase":
		commits, err := gitutil.RevListRange(dir, targetSHA, headSHA)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		// Oldest first.
		for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
			commits[i], commits[j] = commits[j], commits[i]
		}
		onto := targetSHA
		for _, sha := range commits {
			parents, err := gitutil.CommitParents(dir, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if len(parents) > 1 {
				return c.fail(protocol.ExitUsage,
					"the MR contains merge commit %.10s; a rebase merge needs linear history — use --strategy merge or squash", sha)
			}
			base := onto // root commit: replay against the new tip itself
			if len(parents) == 1 {
				base = parents[0]
			}
			tree, conflict, err := gitutil.MergeTreeOnto(dir, base, onto, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if conflict {
				return c.fail(protocol.ExitUsage,
					"commit %.10s does not apply cleanly onto %s; rebase locally and re-push", sha, mr.TargetRef)
			}
			aName, aEmail, aDate, err := gitutil.AuthorIdent(dir, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			msg, err := gitutil.CommitMessage(dir, sha)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			onto, err = gitutil.CommitTreeIdent(dir, tree, []string{onto},
				aName, aEmail, aDate, c.User.Username, mergerEmail, msg)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
		}
		newSHA = onto
	}

	// CAS so a concurrent push between our read and this write fails the
	// merge instead of silently discarding the push.
	if err := gitutil.UpdateRefCAS(dir, targetRef, newSHA, targetSHA); err != nil {
		return c.fail(protocol.ExitFailure, "target branch moved during merge; retry: %v", err)
	}
	if err := c.Store.MarkMerged(mr.ID, targetSHA, c.User.ID, ""); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "mr.merged", fmt.Sprintf(`{"number":%d,"sha":%q}`, mr.Number, newSHA))
	// Merges bypass receive-pack, so the commit-message issue actions
	// (closes #N, references) run here for the newly landed commits. The
	// description is scanned after them, so a commit wins the attribution
	// when both name the same issue.
	if mr.TargetRef == repo.DefaultBranch {
		ProcessCommitMessages(c.Store, dir, repo, c.User.ID, targetSHA, newSHA)
		ProcessMRDescription(c.Store, repo, mr, c.User.ID)
		RecordLandedCommits(c.Store, dir, repo, targetSHA, newSHA)
	}
	// A merge moves the ref directly, so it never reaches post-receive and
	// none of the ref-update work fires on its own. The event webhooks
	// subscribe to, and the branch's CI jobs, happen here instead.
	c.Store.RecordEvent(repo.ID, c.User.ID, "push", fmt.Sprintf(
		`{"ref":%q,"old":%q,"new":%q,"forced":false,"deleted":false}`,
		targetRef, targetSHA, newSHA))
	QueueBranchBuilds(c.Store, c.Cfg.Server.Root, c.Cfg.Server.SiteURL,
		repo, c.User.ID, mr.TargetRef, newSHA, time.Now())
	c.Store.MarkMirrorsDirty(repo.ID, "push")
	if parts, err := c.Store.MRParticipants(mr.ID); err == nil {
		notifyUsers(c, parts, mrSubject(repo, mr.Number, mr.Title),
			notifyBody(c, fmt.Sprintf("merged !%d into %s (%s)", mr.Number, mr.TargetRef, strategy), "", fmt.Sprintf("%s/mrs/%d", repo.Path(), mr.Number)))
	}
	return c.emit(map[string]any{"number": mr.Number, "strategy": strategy, "sha": newSHA}, func(w io.Writer) {
		fmt.Fprintf(w, "merged %s!%d into %s (%s) at %.10s\n", repo.Path(), mr.Number, mr.TargetRef, strategy, newSHA)
	})
}

// reviewGates enforces require_approvals (fresh, non-author, latest review
// per reviewer; a fresh request-changes blocks), CODEOWNERS coverage, and
// require_resolved. Returns -1 to proceed.
func (c *Ctx) reviewGates(repo store.Repo, mr store.MR, dir, targetSHA, headSHA string) int {
	set := repo.Settings
	if set.RequireApprovals == 0 && !set.RequireResolved {
		return -1
	}

	if set.RequireApprovals > 0 {
		reviews, err := c.Store.ListMRReviews(mr.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		// Latest fresh review per reviewer decides their stance.
		latest := map[string]string{}
		for _, r := range reviews {
			if r.Stale || r.Reviewer == mr.Author {
				continue
			}
			latest[r.Reviewer] = r.Verdict
		}
		var approvers []string
		var blockers []string
		for who, verdict := range latest {
			switch verdict {
			case "approve":
				approvers = append(approvers, who)
			case "request_changes":
				blockers = append(blockers, who)
			}
		}
		if len(blockers) > 0 {
			slices.Sort(blockers)
			return c.fail(protocol.ExitDenied,
				"%s requested changes on !%d; resolve their review before merging", strings.Join(blockers, ", "), mr.Number)
		}
		if len(approvers) < set.RequireApprovals {
			return c.fail(protocol.ExitDenied,
				"%s requires %d fresh approval(s); !%d has %d", repo.Path(), set.RequireApprovals, mr.Number, len(approvers))
		}

		// CODEOWNERS: every owned changed file needs an approval from one
		// of its owners.
		content, err := gitutil.ReadBlob(dir, "refs/heads/"+mr.TargetRef, "CODEOWNERS", 1<<20)
		if err != nil {
			content, err = gitutil.ReadBlob(dir, "refs/heads/"+mr.TargetRef, ".gitbay/CODEOWNERS", 1<<20)
		}
		if err == nil && len(content) > 0 {
			rules := policy.ParseCodeowners(string(content))
			base, err := gitutil.MergeBase(dir, targetSHA, headSHA)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			files, err := gitutil.DiffFiles(dir, base, headSHA)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			approved := map[string]bool{}
			for _, a := range approvers {
				approved[a] = true
			}
			missing := map[string][]string{} // owner-set key -> example paths
			for _, f := range files {
				owners := policy.OwnersFor(rules, f)
				if owners == nil {
					continue
				}
				ok := false
				for _, o := range owners {
					if approved[o] {
						ok = true
						break
					}
				}
				if !ok {
					key := strings.Join(owners, ",")
					if len(missing[key]) < 3 {
						missing[key] = append(missing[key], f)
					}
				}
			}
			if len(missing) > 0 {
				var parts []string
				for owners, paths := range missing {
					parts = append(parts, fmt.Sprintf("%s (owned by %s)", strings.Join(paths, ", "), owners))
				}
				slices.Sort(parts)
				return c.fail(protocol.ExitDenied,
					"CODEOWNERS approval missing for: %s", strings.Join(parts, "; "))
			}
		}
	}

	if set.RequireResolved {
		n, err := c.Store.UnresolvedThreadCount(mr.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if n > 0 {
			return c.fail(protocol.ExitDenied,
				"%s requires review threads resolved; !%d has %d open (mr threads %s %d)", repo.Path(), mr.Number, n, repo.Path(), mr.Number)
		}
	}
	return -1
}

func runMRClose(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: mr close <owner/name> <n>")
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if mr.Author != c.User.Username && !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can close this MR")
	}
	if mr.State == "merged" || mr.State == "closed" {
		return c.fail(protocol.ExitUsage, "MR !%d is already %s", mr.Number, mr.State)
	}
	if err := c.Store.MarkClosed(mr.ID, c.User.ID, ""); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": mr.Number, "state": "closed"}, func(w io.Writer) {
		fmt.Fprintf(w, "closed %s!%d\n", repo.Path(), mr.Number)
	})
}
