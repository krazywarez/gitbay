package httpd

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// mrPageData mirrors the anonymous struct the mr handler renders with.
type mrPageData struct {
	repoPage
	MR              store.MR
	View            string
	BodyHTML        template.HTML
	Checks          []store.Check
	Combined        string
	Comments        []renderedComment
	Reviews         []reviewRow
	DiffFiles       []diffFile
	Stat            diffStat
	Commits         []struct{}
	Branches        []gitutil.Ref
	CanEdit         bool
	CanWrite        bool
	Unresolved      int
	Revisions       []store.MRHead
	Notice          string
	DetachedThreads []diffThread
	Gates           *control.GatesOut
}

func renderMR(t *testing.T, m store.MR, reviews []store.MRReview, checks []store.Check) string {
	rows := make([]reviewRow, 0, len(reviews))
	for _, r := range reviews {
		// The page test renders reviews that count; whether a given
		// reviewer's does is decided by access, which e2e covers.
		rows = append(rows, reviewRow{MRReview: r, Counts: true})
	}
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "mr.html", mrPageData{
		repoPage: testRepoPage(), MR: m, View: "conversation",
		Reviews: rows, Checks: checks, Combined: "",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func testMR(state string) store.MR {
	return store.MR{Number: 42, Title: "org native rendering", Author: "cmc", State: state,
		SourcePath: "krz/hutch", SourceRef: "org-native-rendering", TargetRef: "main",
		HeadSHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b"}
}

// The header states what happened to the MR. "wants to merge" is only true
// while it is still open.
func TestMRHeaderByState(t *testing.T) {
	open := renderMR(t, testMR("open"), nil, nil)
	if !strings.Contains(open, "wants to merge") {
		t.Errorf("open MR does not say wants to merge:\n%s", open)
	}

	merged := testMR("merged")
	merged.MergedAt, merged.MergedBy = "2026-08-27T14:03:11.000Z", "cmc"
	out := renderMR(t, merged, nil, nil)
	for _, want := range []string{"merged", "krz/hutch:org-native-rendering", "2026-08-27 14:03"} {
		if !strings.Contains(out, want) {
			t.Errorf("merged header missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wants to merge") {
		t.Errorf("merged MR still wants to merge:\n%s", out)
	}

	closed := testMR("closed")
	closed.ClosedAt, closed.ClosedBy = "2026-08-27T14:03:11.000Z", "cmc"
	out = renderMR(t, closed, nil, nil)
	if !strings.Contains(out, "without merging") || strings.Contains(out, "wants to merge") {
		t.Errorf("closed header:\n%s", out)
	}

	// Imports and pre-0029 merges carry no stamp; the wording drops the
	// claim rather than inventing a time.
	out = renderMR(t, testMR("merged"), nil, nil)
	if strings.Contains(out, "wants to merge") || strings.Contains(out, " on 20") {
		t.Errorf("unstamped merged header:\n%s", out)
	}
}

// Approvals and checks carry their times in the aside, so reading the MR
// does not mean opening the build.
func TestMRAsideTimestamps(t *testing.T) {
	out := renderMR(t, testMR("open"),
		[]store.MRReview{{Reviewer: "cmc", Verdict: "approve", CreatedAt: "2026-08-27T14:03:11.000Z"}},
		[]store.Check{
			{CommitStatus: store.CommitStatus{Context: "ci/test", State: "success",
				UpdatedAt: "2026-08-27T14:05:00.000Z"}, Duration: 72 * time.Second, Build: 60},
			{CommitStatus: store.CommitStatus{Context: "external/lint", State: "success",
				UpdatedAt: "2026-08-27T14:06:00.000Z"}},
		})
	for _, want := range []string{"2026-08-27 14:03", "2026-08-27 14:05", "1m12s", "2026-08-27 14:06"} {
		if !strings.Contains(out, want) {
			t.Errorf("aside missing %q:\n%s", want, out)
		}
	}
}

// A job a path filter excluded has no build behind its status: the page
// must show it as skipped rather than linking to a build that never ran
// (#172).
func TestMRChecksRenderSkippedWithoutBuildLink(t *testing.T) {
	out := renderMR(t, testMR("open"), nil, []store.Check{
		{CommitStatus: store.CommitStatus{Context: "ci/unit", State: "skipped",
			Description: "every changed file matched paths-ignore", UpdatedAt: "2026-08-27T14:05:00.000Z"}},
	})
	row := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ci/unit") {
			row = line
		}
	}
	if row == "" || !strings.Contains(row, ">skipped<") {
		t.Fatalf("skipped check not rendered:\n%s", out)
	}
	if strings.Contains(row, "<a href") {
		t.Errorf("skipped check with no build linked anyway: %s", row)
	}
}

// The gates block says what the merge is waiting on before a merge is
// refused (#199): every unmet gate, the approval count, the outstanding
// owners, and whether a fast-forward is possible.
func TestMRGatesRender(t *testing.T) {
	render := func(g *control.GatesOut) string {
		t.Helper()
		var sb strings.Builder
		if err := web.Render(&sb, "mr.html", mrPageData{
			repoPage: testRepoPage(), MR: testMR("open"), View: "conversation", Gates: g,
		}); err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}
	out := render(&control.GatesOut{ApprovalsRequired: 2, Approvals: []string{"bob"},
		OwnersOutstanding: []control.OwnersOut{{Files: []string{"svc.go"}, Owners: []string{"carol"}}},
		Unmet:             []string{"krz/hutch requires 2 fresh approval(s); !42 has 1", "CODEOWNERS approval missing for: svc.go (owned by carol)"}})
	for _, want := range []string{"Merge gates", "requires 2 fresh approval(s)", "approvals: 1 of 2 (bob)", `waiting on <a href="/carol">carol</a>`, "not a fast-forward"} {
		if !strings.Contains(out, want) {
			t.Errorf("gates block missing %q:\n%s", want, out)
		}
	}
	if out := render(&control.GatesOut{FastForward: true}); !strings.Contains(out, "All gates met") || !strings.Contains(out, "fast-forward possible") {
		t.Errorf("met gates not rendered:\n%s", out)
	}
	if out := render(nil); strings.Contains(out, "Merge gates") {
		t.Errorf("gates block on a merge request without gates:\n%s", out)
	}
}
