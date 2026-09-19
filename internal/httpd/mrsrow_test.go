package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

// mrsPageData mirrors the anonymous struct the mrs handler renders with.
type mrsPageData struct {
	repoPage
	State   string
	Query   string
	Filters []listFilter
	Facets  []facetGroup
	MRs     []mrRow
	Older   string
}

func renderMRs(t *testing.T, rows []mrRow, state string) string {
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "mrs.html", mrsPageData{
		repoPage: testRepoPage(), State: state, MRs: rows,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// Each row carries its head's combined check state as a chip linking to
// the diff, so a reviewer can tell what needs attention without opening
// every request (#230).
func TestMRListRowShowsCheck(t *testing.T) {
	out := renderMRs(t, []mrRow{{MR: testMR("open"), Check: "success"}}, "open")
	if !strings.Contains(out, `class="chip check-success"`) {
		t.Errorf("no check chip:\n%s", out)
	}
	if !strings.Contains(out, `/krz/gitbay/mrs/42?view=diff`) {
		t.Errorf("check chip does not link to the diff:\n%s", out)
	}
}

// No checks have reported yet: no chip, not an empty one.
func TestMRListRowHidesEmptyCheck(t *testing.T) {
	out := renderMRs(t, []mrRow{{MR: testMR("open")}}, "open")
	if strings.Contains(out, "check-") {
		t.Errorf("empty check rendered a chip:\n%s", out)
	}
}

// The comment count folds conversation and diff-thread comments (see
// store.MRCommentCounts) and is hidden, not zero, when there are none.
func TestMRListRowShowsCommentCount(t *testing.T) {
	out := renderMRs(t, []mrRow{{MR: testMR("open"), Comments: 3}}, "open")
	if !strings.Contains(out, `title="3 comments"`) {
		t.Errorf("no plural comment count:\n%s", out)
	}
	if !strings.Contains(out, `>3 <span class="vh">comments</span>`) {
		t.Errorf("no comment count text:\n%s", out)
	}

	one := renderMRs(t, []mrRow{{MR: testMR("open"), Comments: 1}}, "open")
	if !strings.Contains(one, `title="1 comment"`) {
		t.Errorf("comment count not singular for 1:\n%s", one)
	}

	none := renderMRs(t, []mrRow{{MR: testMR("open")}}, "open")
	if strings.Contains(none, "vh\">comments</span>") {
		t.Errorf("zero comments rendered a count:\n%s", none)
	}
}
