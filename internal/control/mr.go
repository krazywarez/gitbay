package control

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"repo", "fork"},
		Summary: "fork a repository under your account: repo fork <owner/name> [--name <n>]", Run: runRepoFork})
	register(Command{Path: []string{"repo", "settings", "require-signed"},
		Summary: "require verified commit signatures: repo settings require-signed <owner/name> on|off", Run: runRequireSigned})
	register(Command{Path: []string{"mr", "create"},
		Summary:    "open a merge request: mr create <target owner/name> --source [owner/name:]<branch> --target <branch> --title <t> [--body <b> | --file -]",
		ReadsStdin: true, Run: runMRCreate})
	register(Command{Path: []string{"mr", "list"},
		Summary: "list merge requests: mr list <owner/name> [--state open|merged|closed|source_gone|all]", ReadOnly: true, Run: runMRList})
	register(Command{Path: []string{"mr", "show"},
		Summary: "show a merge request: mr show <owner/name> <n>", ReadOnly: true, Run: runMRShow})
	register(Command{Path: []string{"mr", "diff"},
		Summary: "show the diff: mr diff <owner/name> <n>", ReadOnly: true, Run: runMRDiff})
	register(Command{Path: []string{"mr", "comment"},
		Summary:    "comment: mr comment <owner/name> <n> [--message <m> | --file -]",
		ReadsStdin: true, Run: runMRComment})
	register(Command{Path: []string{"mr", "review"},
		Summary: "review: mr review <owner/name> <n> --approve|--request-changes|--comment", Run: runMRReview})
	register(Command{Path: []string{"mr", "merge"},
		Summary: "merge: mr merge <owner/name> <n> [--strategy ff|merge|squash|rebase]", Run: runMRMerge})
	register(Command{Path: []string{"mr", "close"},
		Summary: "close without merging: mr close <owner/name> <n>", Run: runMRClose})
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
	var path, source, target, title, body, file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source", "--target", "--title", "--body", "--file":
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
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
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
	n, err := c.Store.CreateMR(repo.ID, c.User.ID, srcRepo.ID, srcBranch, target, title, b, headSHA)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// Fetch the head into the target so the target owns the objects.
	dstDir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if err := gitutil.FetchInto(dstDir, srcDir, headSHA, mrHeadRef(n)); err != nil {
		return c.fail(protocol.ExitFailure, "recording MR head: %v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "mr.created", fmt.Sprintf(`{"number":%d}`, n))
	return c.emit(map[string]any{"number": n, "head_sha": headSHA}, func(w io.Writer) {
		fmt.Fprintf(w, "created %s!%d (%s -> %s)\n", repo.Path(), n, source, target)
	})
}

type mrOut struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Author    string `json:"author"`
	Source    string `json:"source"` // owner/name:branch, or branch, "" if gone
	TargetRef string `json:"target_ref"`
	HeadSHA   string `json:"head_sha"`
	Body      string `json:"body,omitempty"`
	CreatedAt string `json:"created_at"`
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
		Source: src, TargetRef: m.TargetRef, HeadSHA: m.HeadSHA, CreatedAt: m.CreatedAt}
	if withBody {
		o.Body = m.Body
	}
	return o
}

func runMRList(c *Ctx, args []string) int {
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
		return c.fail(protocol.ExitUsage, "usage: mr list <owner/name> [--state open|merged|closed|source_gone|all]")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	mrs, err := c.Store.ListMRs(repo.ID, state)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var ds []mrOut
	for _, m := range mrs {
		ds = append(ds, mrToOut(repo, m, false))
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "!%d\t%s\t%s\t%s -> %s\n", d.Number, d.State, d.Title, d.Source, d.TargetRef)
		}
	})
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
	type commentOut struct {
		Author    string `json:"author"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	type reviewOut struct {
		Reviewer string `json:"reviewer"`
		Verdict  string `json:"verdict"`
		Stale    bool   `json:"stale"`
	}
	var cs []commentOut
	for _, cm := range comments {
		cs = append(cs, commentOut{cm.Author, cm.Body, cm.CreatedAt})
	}
	var rs []reviewOut
	for _, r := range reviews {
		rs = append(rs, reviewOut{r.Reviewer, r.Verdict, r.Stale})
	}
	d := struct {
		mrOut
		Comments []commentOut `json:"comments,omitempty"`
		Reviews  []reviewOut  `json:"reviews,omitempty"`
	}{mrToOut(repo, mr, true), cs, rs}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "!%d %s [%s] by %s\n%s -> %s @ %.10s\n", d.Number, d.Title, d.State, d.Author, d.Source, d.TargetRef, d.HeadSHA)
		if d.Body != "" {
			fmt.Fprintf(w, "\n%s\n", d.Body)
		}
		for _, r := range rs {
			stale := ""
			if r.Stale {
				stale = " (stale)"
			}
			fmt.Fprintf(w, "review: %s %s%s\n", r.Reviewer, r.Verdict, stale)
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
	base, err := gitutil.MergeBase(dir, "refs/heads/"+mr.TargetRef, head)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	patch, err := gitutil.Diff(dir, base, head, 4<<20)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	fmt.Fprint(c.Stdout, patch)
	return protocol.ExitOK
}

func runMRComment(c *Ctx, args []string) int {
	var rest []string
	var message, file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message", "--file":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			if args[i] == "--message" {
				message = args[i+1]
			} else {
				file = args[i+1]
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	repo, mr, code := mrRef(c, rest, policy.CanRead)
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
	if err := c.Store.AddMRComment(mr.ID, c.User.ID, body); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
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
	if mr.State != "open" {
		return c.fail(protocol.ExitUsage, "MR !%d is %s", mr.Number, mr.State)
	}
	if err := c.Store.AddMRReview(mr.ID, c.User.ID, verdict, mr.HeadSHA); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
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
	if err := c.Store.SetMRState(mr.ID, "merged"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "mr.merged", fmt.Sprintf(`{"number":%d,"sha":%q}`, mr.Number, newSHA))
	return c.emit(map[string]any{"number": mr.Number, "strategy": strategy, "sha": newSHA}, func(w io.Writer) {
		fmt.Fprintf(w, "merged %s!%d into %s (%s) at %.10s\n", repo.Path(), mr.Number, mr.TargetRef, strategy, newSHA)
	})
}

func runMRClose(c *Ctx, args []string) int {
	repo, mr, code := mrRef(c, args, policy.CanRead)
	if code >= 0 {
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
	if err := c.Store.SetMRState(mr.ID, "closed"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"number": mr.Number, "state": "closed"}, func(w io.Writer) {
		fmt.Fprintf(w, "closed %s!%d\n", repo.Path(), mr.Number)
	})
}
