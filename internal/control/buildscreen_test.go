package control

import (
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestBuildShowPlainPinned(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "test", "a136534ff9a136534ff9a136534ff9a136534ff9", "main", `["go vet ./...","go test ./..."]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimBuild([]int64{repo.ID}, false); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.SetBuildFailure(id, 2, "exit 1"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishBuild(id, "failure"); err != nil {
		t.Fatal(err)
	}
	b, err := st.BuildByID(id)
	if err != nil {
		t.Fatal(err)
	}
	c, out, errOut := mrTestCtx(st, store.User{ID: uid, Username: "alice"})
	if code := Dispatch(c, []string{"build", "show", repo.Path(), strconv.FormatInt(b.Number, 10)}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "build-show", out.String())
}

func TestBuildShowScreenFailed(t *testing.T) {
	c := screenCtx(100, false)
	d := BuildOut{Number: 83, Job: "daily-pull", Status: "failure", SHA: "355b04066b", Ref: "main",
		Steps: []string{"setup", "pull", "publish"}, FailedStep: 2, FailedReason: "exit 1", DurationS: 74}
	s := buildShowScreen(c, store.Repo{OwnerName: "krz", Name: "omaha-metro-blotter"}, d, nil)
	steps := s.sections[0]
	if steps.title != "Steps" || steps.n != 3 {
		t.Fatalf("steps = %+v", steps)
	}
	for i, want := range []string{"✓", "✗", "○"} {
		if g := steps.rows[i].cells[0]; g.s != want {
			t.Errorf("step %d glyph = %q, want %q", i+1, g.s, want)
		}
	}
	if len(s.actions) != 1 || s.actions[0].argv[1] != "log" {
		t.Errorf("actions = %+v", s.actions)
	}
	out := renderString(c, s)
	if !strings.Contains(out, "✗  pull     exit 1\n") {
		t.Errorf("failed step line:\n%s", out)
	}
	checkActions(t, s)
}

func TestBuildShowScreenNamesItsMR(t *testing.T) {
	c := screenCtx(100, false)
	d := BuildOut{Number: 1781, Job: "test", Status: "running", SHA: "ee77220399", Ref: "cli-views-1"}
	out := renderString(c, buildShowScreen(c, store.Repo{OwnerName: "krz", Name: "gitbay"}, d, &store.MR{Number: 552, Title: "screen renderer"}))
	if !strings.Contains(out, "MR:      !552  screen renderer\n") || !strings.Contains(out, "State:   ◐  running\n") {
		t.Errorf("render:\n%s", out)
	}
}
