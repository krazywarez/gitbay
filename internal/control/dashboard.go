package control

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"dashboard"},
		Summary:  "one read for the account dashboard: pinned repos, open MRs, assigned issues, recent builds",
		ReadOnly: true, Run: runDashboard})
	register(Command{Path: []string{"feed"},
		Summary:  "activity on repositories you can reach: feed [--limit <n>] [--cursor <c>]",
		ReadOnly: true, Run: runFeed})
}

// dashboardItem is one open issue or MR row, with its repo resolved so a
// client renders the aggregate without further reads.
type dashboardItem struct {
	Repo      string `json:"repo"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

func runDashboard(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: dashboard")
	}
	type pinnedOut struct {
		Path        string `json:"path"`
		Visibility  string `json:"visibility"`
		Description string `json:"description,omitempty"`
		Archived    bool   `json:"archived,omitempty"`
	}
	type buildOut struct {
		Repo       string `json:"repo"`
		Number     int64  `json:"number"`
		Job        string `json:"job"`
		Status     string `json:"status"`
		SHA        string `json:"sha"`
		Ref        string `json:"ref"`
		CreatedAt  string `json:"created_at"`
		FinishedAt string `json:"finished_at,omitempty"`
	}
	type out struct {
		Pinned   []pinnedOut     `json:"pinned"`
		MRs      []dashboardItem `json:"open_mrs"`
		Assigned []dashboardItem `json:"assigned_issues"`
		Builds   []buildOut      `json:"builds"`
	}
	d := out{Pinned: []pinnedOut{}, MRs: []dashboardItem{}, Assigned: []dashboardItem{}, Builds: []buildOut{}}

	pinned, err := c.Store.PinnedRepos(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, r := range pinned {
		grant, err := c.Store.AccessRole(r.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !policy.CanRead(c.User, r, grant) {
			continue
		}
		desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		d.Pinned = append(d.Pinned, pinnedOut{r.Path(), r.Visibility, desc, r.Settings.Archived})
	}

	mrs, err := c.Store.DashboardMRs(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, m := range mrs {
		d.MRs = append(d.MRs, dashboardItem{m.RepoPath, m.Number, m.Title, m.Author, m.State, m.UpdatedAt})
	}

	assigned, err := c.Store.AssignedIssues(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, i := range assigned {
		d.Assigned = append(d.Assigned, dashboardItem{i.RepoPath, i.Number, i.Title, i.Author, i.State, i.UpdatedAt})
	}

	builds, err := c.Store.RecentBuilds(c.User.ID, 20)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, b := range builds {
		d.Builds = append(d.Builds, buildOut{b.RepoPath, b.Number, b.Job, b.Status, b.SHA, b.Ref, b.CreatedAt, b.FinishedAt})
	}

	return c.emit(d, func(w io.Writer) {
		fmt.Fprintln(w, "pinned:")
		for _, p := range d.Pinned {
			mark := ""
			if p.Archived {
				mark = "\t[archived]"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s%s\n", p.Path, p.Visibility, p.Description, mark)
		}
		fmt.Fprintln(w, "open merge requests:")
		for _, m := range d.MRs {
			fmt.Fprintf(w, "  %s!%d\t%s\t%s\n", m.Repo, m.Number, m.Title, m.Author)
		}
		fmt.Fprintln(w, "assigned issues:")
		for _, i := range d.Assigned {
			fmt.Fprintf(w, "  %s#%d\t%s\t%s\n", i.Repo, i.Number, i.Title, i.Author)
		}
		fmt.Fprintln(w, "builds:")
		for _, b := range d.Builds {
			fmt.Fprintf(w, "  %s\t%d\t%s\t%s\t%.10s\t%s\n", b.Repo, b.Number, b.Job, b.Status, b.SHA, b.Ref)
		}
	})
}

// feedDefaultLimit caps a bare `feed` call; pagination reaches further
// back.
const feedDefaultLimit = 50

func runFeed(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "feed", true)
	if code >= 0 {
		return code
	}
	if len(rest) != 0 {
		return c.fail(protocol.ExitUsage, "usage: feed [--limit <n>] [--cursor <c>]")
	}
	if p.limit == 0 {
		p.limit = feedDefaultLimit
	}
	events, err := c.Store.RecentEvents(c.User.ID, p.queryLimit(), p.keyInt())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	events, next := trimPage(p, events, "feed", func(e store.FeedEvent) string {
		return strconv.FormatInt(e.ID, 10)
	})
	type feedOut struct {
		ID        int64           `json:"id"`
		Repo      string          `json:"repo"`
		Actor     string          `json:"actor,omitempty"`
		Kind      string          `json:"kind"`
		Data      json.RawMessage `json:"data,omitempty"`
		CreatedAt string          `json:"created_at"`
	}
	var ds []feedOut
	for _, e := range events {
		d := feedOut{ID: e.ID, Repo: e.RepoPath, Actor: e.Actor, Kind: e.Kind, CreatedAt: e.CreatedAt}
		if json.Valid([]byte(e.Data)) {
			d.Data = json.RawMessage(e.Data)
		}
		ds = append(ds, d)
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.CreatedAt, d.Actor, d.Kind, d.Repo, string(d.Data))
		}
	})
}
