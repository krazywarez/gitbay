package control

import (
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestMRShowPlainPinned(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMRComment(mr.ID, owner.ID, "Looks right.", "md"); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []string{"test", "build"} {
		if err := st.SetCommitStatus(repo.ID, "abc111", ctx, "success", "", "", owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	c, out, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "mr-show", out.String())
}

func mrShowFixture() MRShow {
	d := MRShow{mrOut: mrOut{Number: 552, Title: "wire $PAGER through long views", State: "open",
		Author: "cmc", Source: "cli-pager", TargetRef: "main", Body: "Pages long views.", BodyFormat: "md",
		CreatedAt: "2026-10-01T11:40:00Z"}}
	d.Commits = []CommitOut{{"8f3a1c2aaaaaaaa", "cli: page long output"}, {"2b77e90bbbbbbbb", "control: mark views"}}
	d.Checks = []CheckOut{{Context: "test", State: "pending"}, {Context: "build", State: "success"}}
	d.Gates = &GatesOut{ApprovalsRequired: 1, FastForward: false, Unmet: []string{"needs 1 approval", "behind main"}}
	d.Comments = []commentOut{
		{ID: 1, Author: "alice", Body: "Looks right.", BodyFormat: "md", CreatedAt: "2026-10-01T11:50:00Z", Kind: "comment"},
		{ID: 2, Author: "cmc", Body: "marked ready", CreatedAt: "2026-10-01T11:51:00Z", Kind: "system"},
	}
	return d
}

func actionArgvs(s screen, group string) [][]string {
	var out [][]string
	for _, a := range s.actions {
		if a.group == group {
			out = append(out, a.argv)
		}
	}
	return out
}

func sectionCounts(s screen) map[string]int {
	m := map[string]int{}
	for _, sec := range s.sections {
		m[sec.title] = sec.n
	}
	return m
}

var screenRepo = store.Repo{OwnerName: "krz", Name: "gitbay", DefaultBranch: "main"}

func TestMRShowScreenBehind(t *testing.T) {
	c := screenCtx(100, false)
	s := mrShowScreen(c, screenRepo, mrShowFixture(), []gitutil.NumStat{{Path: "cmd/gitbay/ssh.go", Added: 41, Deleted: 6, Status: "M"}})
	if got := actionArgvs(s, "Unblock"); len(got) != 1 || got[0][1] != "rebase" {
		t.Errorf("Unblock = %q, want mr rebase", got)
	}
	if got := actionArgvs(s, "Merge"); len(got) != 0 {
		t.Errorf("Merge offered with unmet gates: %q", got)
	}
	if n := sectionCounts(s); n["Commits"] != 2 || n["Files"] != 1 || n["Discussion"] != 1 {
		t.Errorf("sections = %v", n)
	}
	checkActions(t, s)
}

func TestMRShowScreenReady(t *testing.T) {
	c := screenCtx(100, false)
	d := mrShowFixture()
	d.Gates = &GatesOut{FastForward: true}
	s := mrShowScreen(c, screenRepo, d, nil)
	if len(actionArgvs(s, "Unblock")) != 0 {
		t.Error("Unblock offered when nothing blocks")
	}
	if got := actionArgvs(s, "Merge"); len(got) != 1 || got[0][1] != "merge" {
		t.Errorf("Merge = %q", got)
	}
	checkActions(t, s)
}

func TestMRShowScreenMergedOffersOnlyReading(t *testing.T) {
	c := screenCtx(100, false)
	d := mrShowFixture()
	d.State, d.Gates = "merged", nil
	s := mrShowScreen(c, screenRepo, d, nil)
	for _, a := range s.actions {
		if a.group != "Read" {
			t.Errorf("merged MR offers %q", a.argv)
		}
	}
}

func TestMRShowScreenRenders(t *testing.T) {
	termNow = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { termNow = time.Now })
	c := screenCtx(100, false)
	c.Term.Here = "krz/gitbay"
	s := mrShowScreen(c, screenRepo, mrShowFixture(), []gitutil.NumStat{{Path: "cmd/gitbay/ssh.go", Added: 41, Deleted: 6, Status: "M"}})
	out := renderString(c, s)
	for _, want := range []string{
		"Merge:   !552  wire $PAGER through long views\n",
		"Gates:   ✗ needs 1 approval · ✗ behind main\n",
		"Pages long views.\n",
		"Files (1)  +41 −6\n",
		"alice  10m ago\n  Looks right.\n",
		"gitbay mr rebase 552",
		"gitbay mr diff 552",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "marked ready") {
		t.Errorf("system event in the discussion:\n%s", out)
	}
}
