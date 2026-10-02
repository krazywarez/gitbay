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
	// Queued marks a merge request with a queued merge.
	Queued bool `json:"queued,omitempty"`
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
	// Queries is each pinned saved query with its first rows.
	Queries []DashboardQuery `json:"queries"`
	// Unread is the notification inbox badge, so a client showing one
	// does not need a second read to fill it.
	Unread int        `json:"unread"`
	Server *ServerOut `json:"server,omitempty"`
	// Queues is admin-only: every background worker's backlog and
	// failures, the operator's view of what is stuck.
	Queues *store.Queues `json:"queues,omitempty"`
}

// dashboardActivity is how many feed lines the dashboard shows at a
// terminal.
const dashboardActivity = 5

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
		d.MRs = append(d.MRs, DashboardItem{m.RepoPath, m.Number, m.Title, m.Author, m.State, m.UpdatedAt, m.Queued})
	}

	reviews, err := c.Store.ReviewQueue(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, m := range reviews {
		d.Reviews = append(d.Reviews, DashboardItem{m.RepoPath, m.Number, m.Title, m.Author, m.State, m.UpdatedAt, m.Queued})
	}

	assigned, err := c.Store.AssignedIssues(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, i := range assigned {
		d.Assigned = append(d.Assigned, DashboardItem{i.RepoPath, i.Number, i.Title, i.Author, i.State, i.UpdatedAt, i.Queued})
	}

	issues, err := c.Store.DashboardIssues(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, i := range issues {
		d.Issues = append(d.Issues, DashboardItem{i.RepoPath, i.Number, i.Title, i.Author, i.State, i.UpdatedAt, i.Queued})
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
	if d.Queries, err = PinnedQueries(c.Store, c.User); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
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

	lines := FeedLines(events)
	return c.emitView(d, func(w io.Writer) {
		heading := func(title string) { fmt.Fprintln(w, title) }
		section := func(title string, rows [][]cell) {
			heading(title)
			if len(rows) == 0 {
				fmt.Fprintln(w, "  none")
				return
			}
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
		}
		itemRows := func(items []DashboardItem, marker string) [][]cell {
			rows := make([][]cell, len(items))
			for i, item := range items {
				page := "issues"
				if marker == "!" {
					page = "mrs"
				}
				ref := cLink(fmt.Sprintf("%s%s%d", item.Repo, marker, item.Number), c.siteURL(item.Repo, page, strconv.FormatInt(item.Number, 10)))
				rows[i] = []cell{ref, cFlex(item.Title), cText(item.Author)}
			}
			return rows
		}

		if d.Unread > 0 {
			fmt.Fprintf(w, "unread notifications: %d\n", d.Unread)
		}
		section("waiting on your review:", itemRows(d.Reviews, "!"))
		section("assigned to you:", itemRows(d.Assigned, "#"))
		section("open merge requests:", itemRows(d.MRs, "!"))
		section("open issues:", itemRows(d.Issues, "#"))
		for _, q := range d.Queries {
			rows := make([][]cell, 0, len(q.Items))
			for _, it := range q.Items {
				rows = append(rows, []cell{cRef(it.Ref()), cFlex(it.Title), cText(it.Author)})
			}
			title := fmt.Sprintf("query %s (%d):", q.Name, q.Count)
			if q.Error != "" {
				title = fmt.Sprintf("query %s: %s", q.Name, q.Error)
			}
			section(title, rows)
		}

		pinnedRows := make([][]cell, len(d.Pinned))
		for i, p := range d.Pinned {
			cells := []cell{cLink(p.Path, c.siteURL(p.Path)), cState(p.Visibility), cFlex(p.Description)}
			if p.Archived {
				cells = c.note(cells, 1, "[archived]", "archived")
			}
			pinnedRows[i] = cells
		}
		section("pinned:", pinnedRows)

		activityRows := make([][]cell, len(lines))
		for i, l := range lines {
			activityRows[i] = []cell{cAge(l.When), cFlex(l.Sentence())}
		}
		section("recent activity:", activityRows)

		buildRows := make([][]cell, len(d.Builds))
		for i, b := range d.Builds {
			buildRows[i] = []cell{cLink(b.Repo, c.siteURL(b.Repo, "builds", strconv.FormatInt(b.Number, 10))), cNum(b.Number), cText(b.Job), cState(b.Status), cRef(fmt.Sprintf("%.10s", b.SHA)), cText(b.Ref)}
		}
		section("builds:", buildRows)

		if d.Server != nil {
			heading("server:")
			fmt.Fprintf(w, "  build %s\n", d.Server.Commit)
		}
		if q := d.Queues; q != nil {
			heading("queues:")

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
				since = stamp(since)
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
	}, func() screen { return dashboardScreen(c, d, lines) })
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
	lines := FeedLines(events)
	return c.emitPageView(p, ds, next, func(w io.Writer) {
		tb := c.table(w, "WHEN", "EVENT")
		for _, l := range lines {
			tb.row(cAge(l.When), cFlex(l.Sentence()))
		}
		tb.flush()
	}, func() screen {
		rows := make([]row, len(lines))
		for i, l := range lines {
			rows[i] = rowOf(l.termCells(c)...)
		}
		return listScreen("Activity", rows)
	})
}

// dashboardScreen is dashboard at a terminal: what waits on the viewer
// first, then open merge requests, failed builds, a few lines of
// activity and the pinned repositories. The operator's queues are
// admin stats'; a background failure shows as one header line.
func dashboardScreen(c *Ctx, d DashboardOut, lines []FeedLine) screen {
	var s screen
	s.fields = append(s.fields, field{"User", []cell{cText(c.User.Username)}})
	if host := c.Cfg.SiteHost(); host != "" {
		s.fields = append(s.fields, field{"Instance", []cell{cText(host)}})
	}
	if q := d.Queues; q != nil {
		if bad := q.Webhooks.Failed + q.Mail.Failed + q.Push.Failed + q.Mirrors.Errors + q.Deps.Errors; bad > 0 {
			s.fields = append(s.fields, field{"Problems", []cell{cGlyph("failed"), cText(fmt.Sprintf("%d failing in the background", bad))}})
		}
	}
	if d.Unread > 0 {
		s.fields = append(s.fields, field{"Inbox", []cell{cYou(), cText(fmt.Sprintf("%d unread", d.Unread))}})
	}

	item := func(it DashboardItem, marker, page string, you bool) row {
		lead := cell{kind: kindGlyph}
		if you {
			lead = cYou()
		}
		ref := cLink(fmt.Sprintf("%s%s%d", it.Repo, marker, it.Number), c.siteURL(it.Repo, page, strconv.FormatInt(it.Number, 10)))
		return rowOf(ref, lead, cFlex(it.Title), cMeta(it.Author))
	}
	items := func(title string, its []DashboardItem, marker, page string, you bool) section {
		sec := section{title: title, n: len(its)}
		for _, it := range its {
			sec.rows = append(sec.rows, item(it, marker, page, you))
		}
		return sec
	}
	s.sections = append(s.sections,
		items("Review requested", d.Reviews, "!", "mrs", true),
		items("Assigned issues", d.Assigned, "#", "issues", true),
		items("Open merge requests", d.MRs, "!", "mrs", false),
	)
	for _, q := range d.Queries {
		sec := section{title: q.Name, n: q.Count, more: []string{"query", "run", q.Name}}
		if q.Error != "" {
			sec.note, sec.empty = q.Error, true
		}
		for _, it := range q.Items {
			sec.rows = append(sec.rows, rowOf(cRef(it.Ref()), cFlex(it.Title), cMeta(it.Author)))
		}
		s.sections = append(s.sections, sec)
	}

	// A job's latest build is the one that counts: a failure a later
	// build of the same job and ref has replaced is not shown.
	failed := section{title: "Failed builds"}
	passed := 0
	seen := map[string]bool{}
	var firstFailed *DashboardBuild
	for i, b := range d.Builds {
		key := b.Repo + "\x00" + b.Job + "\x00" + b.Ref
		if seen[key] {
			continue
		}
		seen[key] = true
		switch b.Status {
		case "success":
			passed++
		case "failure", "error":
			n := strconv.FormatInt(b.Number, 10)
			failed.n++
			failed.rows = append(failed.rows, rowOf(cLink(n, c.siteURL(b.Repo, "builds", n)), cGlyph(b.Status),
				cFlex(b.Job+"  "+b.Ref), cMeta(b.Repo, relAge(b.CreatedAt, termNow()))))
			if firstFailed == nil {
				firstFailed = &d.Builds[i]
			}
		}
	}
	s.sections = append(s.sections, failed)

	activity := section{title: "Recent activity", n: len(lines), more: []string{"feed"}}
	for _, l := range lines[:min(len(lines), dashboardActivity)] {
		activity.rows = append(activity.rows, rowOf(l.termCells(c)...))
	}
	if passed > 0 {
		word := "builds"
		if passed == 1 {
			word = "build"
		}
		activity.note, activity.empty = fmt.Sprintf("%d %s passed", passed, word), true
	}
	s.sections = append(s.sections, activity)

	if len(d.Pinned) > 0 {
		paths := make([]string, len(d.Pinned))
		for i, p := range d.Pinned {
			paths[i] = p.Path
		}
		s.sections = append(s.sections, section{title: "Pinned", n: len(d.Pinned), rows: []row{rowOf(cMeta(strings.Join(paths, "  ")))}})
	}

	if len(d.Reviews) > 0 {
		it := d.Reviews[0]
		s.actions = append(s.actions, action{"Next", []string{"mr", "show", it.Repo, strconv.FormatInt(it.Number, 10)}})
	}
	if len(d.Assigned) > 0 {
		it := d.Assigned[0]
		s.actions = append(s.actions, action{"Next", []string{"issue", "show", it.Repo, strconv.FormatInt(it.Number, 10)}})
	}
	if firstFailed != nil {
		s.actions = append(s.actions, action{"Next", []string{"build", "log", firstFailed.Repo, strconv.FormatInt(firstFailed.Number, 10)}})
	}
	s.actions = append(s.actions, action{"More", []string{"feed"}})
	if c.User.IsAdmin {
		s.actions = append(s.actions, action{"Instance", []string{"admin", "stats"}})
	}
	return s
}
