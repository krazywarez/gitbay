package control

import (
	"fmt"
	"io"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"dashboard"},
		Summary:  "one read for the account dashboard: pinned repos, open MRs, assigned issues, recent builds",
		ReadOnly: true, Run: runDashboard})
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
