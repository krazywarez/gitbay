package control

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"account", "export"},
		Summary:  "write your account bundle (profile, repos, issues, MRs) as JSON",
		Usage:    "account export > bundle.json",
		ReadOnly: true, Run: runAccountExport})
	register(Command{Path: []string{"account", "import-bundle"},
		Summary:    "replay an account bundle (see gitbay migrate)",
		Usage:      "account import-bundle [--source <host>] < bundle.json",
		ReadsStdin: true, Run: runAccountImportBundle})
}

// The bundle format doubles as a user-level backup. Keys are never
// exported (trust is per-instance); emails arrive unverified.
const bundleVersion = "gitbay-account/1"

type bundleComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}
type bundleIssue struct {
	Number    int64           `json:"number"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	State     string          `json:"state"`
	Author    string          `json:"author"`
	CreatedAt string          `json:"created_at"`
	Labels    []string        `json:"labels,omitempty"`
	Comments  []bundleComment `json:"comments,omitempty"`
}
type bundleMR struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Author    string `json:"author"`
	SourceRef string `json:"source_ref"`
	TargetRef string `json:"target_ref"`
	// HeadSHA and MergedBase are what a diff is measured between. The
	// bundle carried neither, so every migrated merge request arrived
	// with an empty head and `mr diff` could only fail on it (#128).
	// They are recorded whether or not the objects have been pushed yet;
	// the ref is pointed at the head once they have.
	HeadSHA    string          `json:"head_sha,omitempty"`
	MergedBase string          `json:"merged_base,omitempty"`
	MergedAt   string          `json:"merged_at,omitempty"`
	CreatedAt  string          `json:"created_at"`
	Comments   []bundleComment `json:"comments,omitempty"`
}
type bundleRepo struct {
	Name          string             `json:"name"`
	Visibility    string             `json:"visibility"`
	DefaultBranch string             `json:"default_branch"`
	Description   string             `json:"description,omitempty"`
	Topics        []string           `json:"topics,omitempty"`
	Settings      store.RepoSettings `json:"settings"`
	Issues        []bundleIssue      `json:"issues,omitempty"`
	MRs           []bundleMR         `json:"mrs,omitempty"`
}
type bundle struct {
	Bundle   string        `json:"bundle"`
	Username string        `json:"username"`
	Profile  store.Profile `json:"profile"`
	Emails   []string      `json:"emails,omitempty"`
	Repos    []bundleRepo  `json:"repos"`
}

func runAccountExport(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: account export > bundle.json")
	}
	b := bundle{Bundle: bundleVersion, Username: c.User.Username}
	b.Profile, _ = c.Store.OwnerProfile("user", c.User.ID)
	b.Emails, _ = c.Store.UserEmailAddresses(c.User.ID)
	repos, err := c.Store.ListReposForOwner("user", c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, r := range repos {
		br := bundleRepo{
			Name:          r.Name,
			Visibility:    r.Visibility,
			DefaultBranch: r.DefaultBranch,
			Description:   gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name)),
			Settings:      r.Settings,
		}
		br.Topics, _ = c.Store.ListTopics(r.ID)
		issues, _ := c.Store.ListIssues(r.ID, "all", 0, 0)
		for i := len(issues) - 1; i >= 0; i-- { // ascending numbers
			iss := issues[i]
			full, err := c.Store.IssueByNumber(r.ID, iss.Number)
			if err != nil {
				continue
			}
			bi := bundleIssue{Number: full.Number, Title: full.Title, Body: full.Body,
				State: full.State, Author: full.Author, CreatedAt: full.CreatedAt, Labels: full.Labels}
			if cs, err := c.Store.ListIssueComments(full.ID); err == nil {
				for _, cm := range cs {
					bi.Comments = append(bi.Comments, bundleComment{cm.Author, cm.Body, cm.CreatedAt})
				}
			}
			br.Issues = append(br.Issues, bi)
		}
		mrs, _ := c.Store.ListMRs(r.ID, "all", 0, 0)
		for i := len(mrs) - 1; i >= 0; i-- {
			m := mrs[i]
			bm := bundleMR{Number: m.Number, Title: m.Title, Body: m.Body, State: m.State,
				Author: m.Author, SourceRef: m.SourceRef, TargetRef: m.TargetRef,
				HeadSHA: m.HeadSHA, MergedBase: m.MergedBase, MergedAt: m.MergedAt,
				CreatedAt: m.CreatedAt}
			if cs, err := c.Store.ListMRComments(m.ID); err == nil {
				for _, cm := range cs {
					bm.Comments = append(bm.Comments, bundleComment{cm.Author, cm.Body, cm.CreatedAt})
				}
			}
			br.MRs = append(br.MRs, bm)
		}
		b.Repos = append(b.Repos, br)
	}
	enc := json.NewEncoder(c.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}

func migAttribution(src, kind, author, date string, n int64) string {
	return fmt.Sprintf("> migrated %s %s#%d — %s, %.10s\n\n", kind, src, n, author, date)
}

// runAccountImportBundle replays a bundle under the calling user. Repos are
// created empty (the CLI pushes git data with the user's own key); issues,
// MRs, and comments arrive attributed inline. Push-blocking policies
// (require_signed_commits, protected_branches) are deferred and reported so
// the git push that follows cannot be refused by them. Resumable: markers
// skip everything already imported.
func runAccountImportBundle(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--source"}, MaxPos: 0, Usage: "account import-bundle [--source <host>] < bundle.json"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	src := f.Value("--source")
	if src == "" {
		src = "the previous instance"
	}
	var b bundle
	if err := json.NewDecoder(io.LimitReader(c.Stdin, 512<<20)).Decode(&b); err != nil {
		return c.fail(protocol.ExitUsage, "bundle does not parse: %v", err)
	}
	if b.Bundle != bundleVersion {
		return c.fail(protocol.ExitUsage, "unsupported bundle %q (want %s)", b.Bundle, bundleVersion)
	}

	if b.Profile.Description != "" || b.Profile.Website != "" ||
		b.Profile.About != "" || len(b.Profile.Links) > 0 {
		c.Store.SetOwnerProfile("user", c.User.ID, b.Profile)
	}
	for _, addr := range b.Emails {
		c.Store.AddEmail(c.User.ID, addr, "", false) // unverified; re-verify here
	}

	type deferred struct {
		Repo     string   `json:"repo"`
		Commands []string `json:"commands"`
	}
	var repos, issues, mrs, comments, skipped int
	var deferrals []deferred
	for _, br := range b.Repos {
		path := c.User.Username + "/" + br.Name
		repo, err := c.Store.RepoByPath(path)
		if err != nil {
			id, err := c.Store.CreateRepo("user", c.User.ID, br.Name, br.Visibility)
			if err != nil {
				return c.fail(protocol.ExitFailure, "creating %s: %v", path, err)
			}
			dir := RepoDir(c.Cfg.Server.Root, c.User.Username, br.Name)
			branch := br.DefaultBranch
			if branch == "" {
				branch = "main"
			}
			if err := gitutil.InitBare(dir, branch, HooksDir(c.Cfg.Server.Root)); err != nil {
				c.Store.DeleteRepo(id)
				return c.fail(protocol.ExitFailure, "initializing %s: %v", path, err)
			}
			if br.Description != "" {
				gitutil.WriteDescription(dir, br.Description)
			}
			repo, err = c.Store.RepoByPath(path)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			repos++
		}
		for _, tpc := range br.Topics {
			c.Store.AddTopic(repo.ID, tpc)
		}
		// Settings minus the two that would refuse the git push coming
		// right after this; the user re-applies them once data is in.
		s := br.Settings
		var cmds []string
		if s.RequireSignedCommits {
			cmds = append(cmds, fmt.Sprintf("gitbay repo settings require-signed %s on", path))
			s.RequireSignedCommits = false
		}
		if len(s.ProtectedBranches) > 0 {
			for _, pb := range s.ProtectedBranches {
				cmds = append(cmds, fmt.Sprintf("gitbay repo settings protect %s %s", path, pb))
			}
			s.ProtectedBranches = nil
		}
		if len(cmds) > 0 {
			deferrals = append(deferrals, deferred{path, cmds})
		}
		// An import writes the whole blob: the repository was created a
		// few lines up and nobody else holds settings on it yet.
		s.GitDaemon = false // instance-dependent; opt back in explicitly
		c.Store.UpdateRepoSettings(repo.ID, func(cur *store.RepoSettings) { *cur = s })

		for _, bi := range br.Issues {
			key := fmt.Sprintf("mig-issue:%d", bi.Number)
			if _, seen, _ := c.Store.ImportMarker(repo.ID, key); seen {
				skipped++
				continue
			}
			body := migAttribution(src, "issue", bi.Author, bi.CreatedAt, bi.Number) + bi.Body
			n, err := c.Store.CreateIssue(repo.ID, c.User.ID, bi.Title, body, "md")
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			iss, err := c.Store.IssueByNumber(repo.ID, n)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			for _, l := range bi.Labels {
				c.Store.SetIssueLabel(repo.ID, iss.ID, l, true)
			}
			if bi.State != "open" {
				c.Store.SetIssueState(iss.ID, "closed")
			}
			for _, cm := range bi.Comments {
				c.Store.AddIssueComment(iss.ID, c.User.ID,
					fmt.Sprintf("> %s, %.10s\n\n%s", cm.Author, cm.CreatedAt, cm.Body), "md")
				comments++
			}
			c.Store.SetImportMarker(repo.ID, key, fmt.Sprint(n))
			issues++
		}
		for _, bm := range br.MRs {
			key := fmt.Sprintf("mig-mr:%d", bm.Number)
			if val, seen, _ := c.Store.ImportMarker(repo.ID, key); seen {
				skipped++
				// A bundle is imported before the git push as often as
				// after, so the objects a merge request needs may only
				// have arrived since. Re-running the import is how the
				// head ref gets set, and doing that costs nothing when it
				// is already right.
				if local, err := strconv.ParseInt(val, 10, 64); err == nil {
					setMigratedHead(c, repo, local, bm.HeadSHA)
				}
				continue
			}
			body := migAttribution(src, "merge request", bm.Author, bm.CreatedAt, bm.Number) + bm.Body
			n, err := c.Store.CreateMR(repo.ID, c.User.ID, repo.ID, bm.SourceRef, bm.TargetRef, bm.Title, body, bm.HeadSHA, "md", false)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			mr, err := c.Store.MRByNumber(repo.ID, n)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			switch {
			case bm.State == "merged":
				// MarkMerged, not SetMRState: a merged MR's diff is
				// measured from the base recorded at merge time, and
				// SetMRState leaves that empty.
				c.Store.MarkMerged(mr.ID, bm.MergedBase, 0, bm.MergedAt)
			case bm.State != "open":
				state := bm.State
				if state == "source_gone" {
					state = "closed"
				}
				c.Store.SetMRState(mr.ID, state)
			}
			setMigratedHead(c, repo, n, bm.HeadSHA)
			for _, cm := range bm.Comments {
				c.Store.AddMRComment(mr.ID, c.User.ID,
					fmt.Sprintf("> %s, %.10s\n\n%s", cm.Author, cm.CreatedAt, cm.Body), "md")
				comments++
			}
			c.Store.SetImportMarker(repo.ID, key, fmt.Sprint(n))
			mrs++
		}
	}
	d := map[string]any{
		"repos": repos, "issues": issues, "mrs": mrs, "comments": comments,
		"already_imported": skipped, "deferred_settings": deferrals,
	}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "imported %d repos, %d issues, %d MRs, %d comments (%d already present)\n",
			repos, issues, mrs, comments, skipped)
		if len(deferrals) > 0 {
			fmt.Fprintf(w, "\nafter pushing git data, re-apply the deferred policies:\n")
			for _, df := range deferrals {
				for _, cmd := range df.Commands {
					fmt.Fprintf(w, "  %s\n", cmd)
				}
			}
		}
	})
}

var _ = strings.TrimSpace // placeholder against accidental import drops

// setMigratedHead points refs/merge-requests/<n>/head at the head a
// bundle recorded, once the objects for it are present. `mr diff`
// resolves through that ref, not through the stored head_sha, so a merge
// request whose source branch is gone — every merged one — has nothing to
// diff without it (#128).
//
// Best-effort by design: a bundle is imported before the git push as
// often as after. head_sha is stored either way, and running the import
// again after the push sets the ref.
func setMigratedHead(c *Ctx, repo store.Repo, number int64, headSHA string) {
	if headSHA == "" {
		return
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if !gitutil.HasCommit(dir, headSHA) {
		return
	}
	ref := fmt.Sprintf("refs/merge-requests/%d/head", number)
	if cur, err := gitutil.ResolveRef(dir, ref); err == nil && cur == headSHA {
		return
	}
	gitutil.UpdateRefCAS(dir, ref, headSHA, "")
}
