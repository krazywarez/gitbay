package httpd

import (
	"html/template"
	"strings"
	"testing"
	"time"

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
	Reviews         []store.MRReview
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
}

func renderMR(t *testing.T, m store.MR, reviews []store.MRReview, checks []store.Check) string {
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "mr.html", mrPageData{
		repoPage: testRepoPage(), MR: m, View: "conversation",
		Reviews: reviews, Checks: checks, Combined: "",
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
