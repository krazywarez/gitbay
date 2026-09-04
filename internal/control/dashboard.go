package control

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/buildinfo"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"dashboard"},
		Summary:  "one read for the account dashboard: review queue, assigned and open work, pins, activity, builds",
		Usage:    "dashboard",
		ReadOnly: true, Run: runDashboard})
	register(Command{Path: []string{"feed"},
		Summary:  "activity on repositories you can reach",
		Usage:    "feed [--limit <n>] [--cursor <c>]",
		ReadOnly: true, Run: runFeed})
}

// DashboardItem is one open issue or MR row, with its repo resolved so a
// client renders the aggregate without further reads.
type DashboardItem struct {
	Repo      string `json:"repo"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

// PinnedOut is one pinned repository on the dashboard.
type PinnedOut struct {
	Path        string `json:"path"`
	Visibility  string `json:"visibility"`
	Description string `json:"description,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
}

// DashboardBuild is a build with its repository resolved, which is what
// separates it from BuildOut: the dashboard spans repositories.
type DashboardBuild struct {
	Repo       string `json:"repo"`
	Number     int64  `json:"number"`
	Job        string `json:"job"`
	Status     string `json:"status"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// ServerOut is admin-only. The exact build a host is running narrows down
// which known issues apply to it, so it is not everyone's to read; the
// person who needs it is the operator.
type ServerOut struct {
	Commit string `json:"commit"`
}

// DashboardOut is what dashboard emits: the whole account aggregate in
// one read.
type DashboardOut struct {
	Reviews  []DashboardItem  `json:"review_queue"`
	Assigned []DashboardItem  `json:"assigned_issues"`
	MRs      []DashboardItem  `json:"open_mrs"`
	Issues   []DashboardItem  `json:"open_issues"`
	Pinned   []PinnedOut      `json:"pinned"`
	Activity []FeedOut        `json:"recent_activity"`
	Builds   []DashboardBuild `json:"builds"`
	// Unread is the notification inbox badge, so a client showing one
	// does not need a second read to fill it.
	Unread int        `json:"unread"`
	Server *ServerOut `json:"server,omitempty"`
	// Queues is admin-only: every background worker's backlog and
	// failures, the operator's view of what is stuck.
	Queues *store.Queues `json:"queues,omitempty"`
}

func runDashboard(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: dashboard")
	}
	d := DashboardOut{
		Reviews: []DashboardItem{}, Assigned: []DashboardItem{}, MRs: []DashboardItem{},
		Issues: []DashboardItem{}, Pinned: []PinnedOut{}, Activity: []FeedOut{}, Builds: []DashboardBuild{},
	}

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
		d.Pinned = append(d.Pinned, PinnedOut{r.Path(), r.Visibility, desc, r.Settings.Archived})
	}

	mrs, err := c.Store.DashboardMRs(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, m := range mrs {
		d.MRs = append(d.MRs, DashboardItem{m.RepoPath, m.Number, m.Title, m.Author, m.State, m.UpdatedAt})
	}

	reviews, err := c.Store.ReviewQueue(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, m := range reviews {
		d.Reviews = append(d.Reviews, DashboardItem{m.RepoPath, m.Number, m.Title, m.Author, m.State, m.UpdatedAt})
	}

	assigned, err := c.Store.AssignedIssues(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, i := range assigned {
		d.Assigned = append(d.Assigned, DashboardItem{i.RepoPath, i.Number, i.Title, i.Author, i.State, i.UpdatedAt})
	}

	issues, err := c.Store.DashboardIssues(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, i := range issues {
		d.Issues = append(d.Issues, DashboardItem{i.RepoPath, i.Number, i.Title, i.Author, i.State, i.UpdatedAt})
	}

	events, err := c.Store.RecentEvents(c.User.ID, 20, 0)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d.Activity = feedOutputs(events)

	builds, err := c.Store.RecentBuilds(c.User.ID, 20)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, b := range builds {
		d.Builds = append(d.Builds, DashboardBuild{b.RepoPath, b.Number, b.Job, b.Status, b.SHA, b.Ref, b.CreatedAt, b.FinishedAt})
	}
	d.Unread = c.Store.UnreadNotices(c.User.ID)
	if c.User.IsAdmin {
		d.Server = &ServerOut{Commit: buildinfo.String()}
		q, err := c.Store.QueueStatus()
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		d.Queues = &q
	}

	return c.emit(d, func(w io.Writer) {
		if d.Unread > 0 {
			fmt.Fprintf(w, "unread notifications: %d\n", d.Unread)
		}
		fmt.Fprintln(w, "waiting on your review:")
		printDashboardItems(w, d.Reviews, "!")
		fmt.Fprintln(w, "assigned to you:")
		printDashboardItems(w, d.Assigned, "#")
		fmt.Fprintln(w, "open merge requests:")
		printDashboardItems(w, d.MRs, "!")
		fmt.Fprintln(w, "open issues:")
		printDashboardItems(w, d.Issues, "#")
		fmt.Fprintln(w, "pinned:")
		for _, p := range d.Pinned {
			mark := ""
			if p.Archived {
				mark = "\t[archived]"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s%s\n", p.Path, p.Visibility, p.Description, mark)
		}
		fmt.Fprintln(w, "recent activity:")
		for _, e := range d.Activity {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", e.CreatedAt, e.Actor, e.Kind, e.Repo, string(e.Data))
		}
		fmt.Fprintln(w, "builds:")
		for _, b := range d.Builds {
			fmt.Fprintf(w, "  %s\t%d\t%s\t%s\t%.10s\t%s\n", b.Repo, b.Number, b.Job, b.Status, b.SHA, b.Ref)
		}
		if d.Server != nil {
			fmt.Fprintf(w, "server:\n  build %s\n", d.Server.Commit)
		}
		if q := d.Queues; q != nil {
			fmt.Fprintln(w, "queues:")
			fmt.Fprintf(w, "  webhooks\tpending %d\tretrying %d\tfailed %d\n", q.Webhooks.Pending, q.Webhooks.Retrying, q.Webhooks.Failed)
			for _, it := range q.Webhooks.Items {
				fmt.Fprintf(w, "    %s\t%s\tattempts %d\t%s\n", it.Repo, it.URL, it.Attempts, it.LastError)
			}
			fmt.Fprintf(w, "  mail\tpending %d\tretrying %d\tfailed %d\n", q.Mail.Pending, q.Mail.Retrying, q.Mail.Failed)
			for _, it := range q.Mail.Items {
				fmt.Fprintf(w, "    %s\t%s\tattempts %d\t%s\n", it.Recipient, it.Subject, it.Attempts, it.LastError)
			}
			fmt.Fprintf(w, "  mirrors\tdirty %d\terrors %d\n", q.Mirrors.Dirty, q.Mirrors.Errors)
			for _, it := range q.Mirrors.Items {
				fmt.Fprintf(w, "    %s\t%s\t%s\t%s\n", it.Repo, it.Direction, it.URL, it.LastError)
			}
			fmt.Fprintf(w, "  builds\tpending %d\trunning %d\n", q.Builds.Pending, q.Builds.Running)
			for _, it := range q.Builds.Items {
				fmt.Fprintf(w, "    %s\t%d\t%s\tsince %s\n", it.Repo, it.Number, it.Job, it.StartedAt)
			}
			fmt.Fprintf(w, "  deps\terrors %d\n", q.Deps.Errors)
			for _, it := range q.Deps.Items {
				fmt.Fprintf(w, "    %s\t%s\n", it.Repo, it.LastError)
			}
		}
	})
}

func printDashboardItems(w io.Writer, items []DashboardItem, marker string) {
	for _, item := range items {
		fmt.Fprintf(w, "  %s%s%d\t%s\t%s\n", item.Repo, marker, item.Number, item.Title, item.Author)
	}
}

// feedDefaultLimit caps a bare `feed` call; pagination reaches further
// back.
const feedDefaultLimit = 50

type FeedOut struct {
	ID        int64           `json:"id"`
	Repo      string          `json:"repo"`
	Actor     string          `json:"actor,omitempty"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt string          `json:"created_at"`
}

func feedOutputs(events []store.FeedEvent) []FeedOut {
	ds := make([]FeedOut, 0, len(events))
	for _, e := range events {
		d := FeedOut{ID: e.ID, Repo: e.RepoPath, Actor: e.Actor, Kind: e.Kind, CreatedAt: e.CreatedAt}
		if json.Valid([]byte(e.Data)) {
			d.Data = json.RawMessage(e.Data)
		}
		ds = append(ds, d)
	}
	return ds
}

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
	ds := feedOutputs(events)
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.CreatedAt, d.Actor, d.Kind, d.Repo, string(d.Data))
		}
	})
}
