package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// The dashboard is three columns: pinned repositories with counts, the
// tile strip and queue rows, the activity feed. Tiles carry every queue's
// count; only a non-empty queue lists rows (desktop layout spec).
func TestDashboardTilesAndPins(t *testing.T) {
	var sb strings.Builder
	var base basePage
	base.Viewer = "alice"
	err := web.Render(&sb, "dashboard.html", struct {
		basePage
		Tab      string
		Pins     []pinnedRow
		Reviews  []store.DashboardItem
		Assigned []store.DashboardItem
		MRs      []store.DashboardItem
		Issues   []store.DashboardItem
		Feed     []feedLine
	}{base, "dashboard", []pinnedRow{{Owner: "krz", Name: "gitbay", Issues: 3, MRs: 0, Build: "success"}}, nil, nil, nil,
		[]store.DashboardItem{{RepoPath: "krz/gitbay", Number: 1, Title: "one", Author: "alice", State: "open"}}, nil})
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`<div class="dashgrid">`,
		`<aside class="dashpins" aria-label="Pinned repositories">`,
		`<span class="owner">krz/</span>gitbay</a>`,
		`<b class="wants">3</b>`, `<span class="dot ok"></span>`,
		`<a class="tile wants" href="#issues"><b>1</b><span>open issues</span></a>`,
		`<div class="tile"><b>0</b><span>waiting on your review</span></div>`,
		`<div class="tile"><b>0</b><span>assigned to you</span></div>`,
		`<div class="tile"><b>0</b><span>open merge requests</span></div>`,
		`<h2 id="issues">Open issues <span class="count">1</span></h2>`,
		`<aside class="feedcol" aria-label="Recent activity">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(out, `<h2 class="empty">`) {
		t.Error("an empty queue still renders as a heading; the tile carries it")
	}
}
