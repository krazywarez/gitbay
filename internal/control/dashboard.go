package control

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

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
		Examples: []string{"dashboard"},
		ReadOnly: true, Run: runDashboard})
	register(Command{Path: []string{"feed"},
		Summary: "activity on repositories you can reach",
		Usage:   "feed [--limit <n>] [--cursor <c>]",
		Flags: []Flag{
			{"--limit", "<n>", "rows per page", ""},
			{"--cursor", "<c>", "continue from the previous page", ""},
		},
		Examples: []string{"feed --limit 20"},
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
		return c.usage()
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
		section := func(title string, header []string, rows [][]cell) {
			if c.Term.Cols > 0 {
				fmt.Fprintln(w, c.Term.paint(sgrBold, title))
			} else {
				fmt.Fprintln(w, title)
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "  none")
				return
			}
			if c.Term.Cols == 0 {
				for _, r := range rows {
					parts := make([]string, len(r))
					for i, cl := range r {
						parts[i] = cl.s
						if cl.kind == kindAge {
							parts[i] = stamp(cl.s)
						}
					}
					fmt.Fprintf(w, "  %s\n", strings.Join(parts, "\t"))
				}
				return
			}
			tb := c.table(w, header...)
			for _, r := range rows {
				tb.row(r...)
			}
			tb.flush()
		}
		itemRows := func(items []DashboardItem, marker string) [][]cell {
			rows := make([][]cell, len(items))
			for i, item := range items {
				rows[i] = []cell{cRef(fmt.Sprintf("%s%s%d", item.Repo, marker, item.Number)), cFlex(item.Title), cText(item.Author)}
			}
			return rows
		}

		if d.Unread > 0 {
			fmt.Fprintf(w, "unread notifications: %d\n", d.Unread)
		}
		itemHeader := []string{"REF", "TITLE", "AUTHOR"}
		section("waiting on your review:", itemHeader, itemRows(d.Reviews, "!"))
		section("assigned to you:", itemHeader, itemRows(d.Assigned, "#"))
		section("open merge requests:", itemHeader, itemRows(d.MRs, "!"))
		section("open issues:", itemHeader, itemRows(d.Issues, "#"))

		pinnedRows := make([][]cell, len(d.Pinned))
		for i, p := range d.Pinned {
			cells := []cell{cRef(p.Path), cState(p.Visibility), cFlex(p.Description)}
			if p.Archived {
				cells = append(cells, cText("[archived]"))
			}
			pinnedRows[i] = cells
		}
		section("pinned:", []string{"PATH", "VISIBILITY", "DESCRIPTION"}, pinnedRows)

		activityRows := make([][]cell, len(d.Activity))
		for i, e := range d.Activity {
			activityRows[i] = []cell{cAge(e.CreatedAt), cText(e.Actor), cText(e.Kind), cRef(e.Repo), cFlex(string(e.Data))}
		}
		section("recent activity:", []string{"WHEN", "ACTOR", "KIND", "REPO", "DATA"}, activityRows)

		buildRows := make([][]cell, len(d.Builds))
		for i, b := range d.Builds {
			buildRows[i] = []cell{cRef(b.Repo), cNum(b.Number), cText(b.Job), cState(b.Status), cRef(fmt.Sprintf("%.10s", b.SHA)), cText(b.Ref)}
		}
		section("builds:", []string{"REPO", "#", "JOB", "STATUS", "SHA", "REF"}, buildRows)

		if d.Server != nil {
			fmt.Fprintf(w, "server:\n  build %s\n", d.Server.Commit)
		}
		if q := d.Queues; q != nil {
			fmt.Fprintln(w, "queues:")

			fmt.Fprintf(w, "  webhooks\tpending %d\tretrying %d\tfailed %d\n", q.Webhooks.Pending, q.Webhooks.Retrying, q.Webhooks.Failed)
			twh := c.table(w, "REPO", "URL", "ATTEMPTS", "ERROR")
			for _, it := range q.Webhooks.Items {
				twh.row(cRef("    "+it.Repo), cText(it.URL), cText(fmt.Sprintf("attempts %d", it.Attempts)), cText(it.LastError))
			}
			twh.flush()

			fmt.Fprintf(w, "  mail\tpending %d\tretrying %d\tfailed %d\n", q.Mail.Pending, q.Mail.Retrying, q.Mail.Failed)
			tma := c.table(w, "RECIPIENT", "SUBJECT", "ATTEMPTS", "ERROR")
			for _, it := range q.Mail.Items {
				tma.row(cRef("    "+it.Recipient), cText(it.Subject), cText(fmt.Sprintf("attempts %d", it.Attempts)), cText(it.LastError))
			}
			tma.flush()

			// The device id, not the token: a token is never echoed.
			fmt.Fprintf(w, "  push\tpending %d\tretrying %d\tfailed %d\n", q.Push.Pending, q.Push.Retrying, q.Push.Failed)
			tpu := c.table(w, "DEVICE", "TITLE", "ATTEMPTS", "ERROR")
			for _, it := range q.Push.Items {
				tpu.row(cRef(fmt.Sprintf("    device %d", it.DeviceID)), cText(it.Title), cText(fmt.Sprintf("attempts %d", it.Attempts)), cText(it.LastError))
			}
			tpu.flush()

			fmt.Fprintf(w, "  mirrors\tdirty %d\terrors %d\n", q.Mirrors.Dirty, q.Mirrors.Errors)
			tmi := c.table(w, "REPO", "DIRECTION", "URL", "ERROR")
			for _, it := range q.Mirrors.Items {
				tmi.row(cRef("    "+it.Repo), cText(it.Direction), cText(it.URL), cText(it.LastError))
			}
			tmi.flush()

			fmt.Fprintf(w, "  builds\tpending %d\trunning %d\n", q.Builds.Pending, q.Builds.Running)
			tbq := c.table(w, "REPO", "#", "JOB", "STATUS")
			for _, it := range q.Builds.Items {
				since := it.StartedAt
				if it.Status == "pending" {
					since = it.CreatedAt
				}
				if c.Term.Cols == 0 {
					since = stamp(since)
				} else {
					since = relAge(since, termNow())
				}
				tbq.row(cRef("    "+it.Repo), cNum(it.Number), cText(it.Job), cText(fmt.Sprintf("%s since %s", it.Status, since)))
			}
			tbq.flush()

			fmt.Fprintf(w, "  deps\terrors %d\n", q.Deps.Errors)
			tde := c.table(w, "REPO", "ERROR")
			for _, it := range q.Deps.Items {
				tde.row(cRef("    "+it.Repo), cText(it.LastError))
			}
			tde.flush()
		}
	})
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
		return c.usage()
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
		tb := c.table(w, "WHEN", "ACTOR", "KIND", "REPO", "DATA")
		for _, d := range ds {
			tb.row(cAge(d.CreatedAt), cText(d.Actor), cText(d.Kind), cRef(d.Repo), cFlex(string(d.Data)))
		}
		tb.flush()
	})
}
