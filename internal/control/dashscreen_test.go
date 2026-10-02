package control

import (
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestDashboardPlainPinned(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	// Both merge requests are created in the same second; the dashboard
	// orders by updated_at alone, so give them distinct times.
	if _, err := st.DB.Exec(`UPDATE merge_requests SET updated_at = '2020-01-01T00:00:00Z' WHERE repo_id = ? AND number = 1`, repo.ID); err != nil {
		t.Fatal(err)
	}
	c, out, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"dashboard"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "dashboard", out.String())
}

func dashFixture() DashboardOut {
	return DashboardOut{
		Assigned: []DashboardItem{{Repo: "krz/skunky-art", Number: 33, Title: "LibRedirect listing", Author: "cmc"}},
		MRs:      []DashboardItem{{Repo: "krz/gitbay", Number: 554, Title: "list screens", Author: "cmc"}},
		Builds: []DashboardBuild{
			{Repo: "krz/omaha-metro-blotter", Number: 83, Job: "daily-pull", Status: "failure", Ref: "main", CreatedAt: "2026-09-30T12:00:00Z"},
			{Repo: "krz/gitbay", Number: 1784, Job: "test", Status: "success", Ref: "main", CreatedAt: "2026-10-01T10:00:00Z"},
			{Repo: "krz/gitbay", Number: 1783, Job: "build", Status: "success", Ref: "main", CreatedAt: "2026-10-01T10:00:00Z"},
			{Repo: "krz/gitbay", Number: 1700, Job: "build", Status: "failure", Ref: "main", CreatedAt: "2026-09-01T10:00:00Z"},
		},
		Server: &ServerOut{Commit: "135f300"},
		Queues: &store.Queues{},
	}
}

func TestDashboardScreen(t *testing.T) {
	c := screenCtx(110, false)
	c.User = store.User{Username: "cmc", IsAdmin: true}
	s := dashboardScreen(c, dashFixture(), nil)
	var titles []string
	for _, sec := range s.sections {
		if len(sec.rows) > 0 || sec.empty {
			titles = append(titles, sec.title)
		}
	}
	if want := []string{"Assigned issues", "Open merge requests", "Failed builds", "Recent activity"}; !slices.Equal(titles, want) {
		t.Errorf("sections = %q, want %q", titles, want)
	}
	if n := sectionCounts(s)["Failed builds"]; n != 1 {
		t.Errorf("failed builds = %d, want 1 (an older failure of a job that has since passed is not one)", n)
	}
	out := renderString(c, s)
	for _, unwanted := range []string{"135f300", "Queues", "webhooks"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("admin block %q on the dashboard screen:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "2 builds passed") {
		t.Errorf("no passing-build summary:\n%s", out)
	}
	var cmds []string
	for _, a := range s.actions {
		cmds = append(cmds, strings.Join(a.argv, " "))
	}
	for _, want := range []string{"issue show krz/skunky-art 33", "build log krz/omaha-metro-blotter 83", "feed", "admin stats"} {
		if !slices.Contains(cmds, want) {
			t.Errorf("actions %q lack %q", cmds, want)
		}
	}
	checkActions(t, s)
}

func TestDashboardScreenProblemsLine(t *testing.T) {
	c := screenCtx(110, false)
	c.User = store.User{Username: "cmc", IsAdmin: true}
	d := dashFixture()
	d.Queues.Mirrors.Errors = 1
	out := renderString(c, dashboardScreen(c, d, nil))
	if !strings.Contains(out, "Problems:  ✗  1 failing in the background\n") {
		t.Errorf("no problems line:\n%s", out)
	}
}

func TestDashboardScreenNonAdmin(t *testing.T) {
	c := screenCtx(110, false)
	c.User = store.User{Username: "bob"}
	d := dashFixture()
	d.Server, d.Queues = nil, nil
	for _, a := range dashboardScreen(c, d, nil).actions {
		if a.argv[0] == "admin" {
			t.Errorf("non-admin offered %q", a.argv)
		}
	}
}
