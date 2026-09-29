package httpd

import (
	"html/template"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

func renderSuggestion(t *testing.T, sg *control.SuggestionOut, canApply bool) string {
	t.Helper()
	view := newSuggestionView(sg, canApply, "gitbay mr apply-suggestion alice/app 1 7")
	var sb strings.Builder
	if err := web.Render(&sb, "mr.html", mrPageData{
		repoPage: testRepoPage(), MR: testMR("open"), View: "conversation",
		DetachedThreads: []diffThread{{ID: 7, Suggestion: view,
			Comments: []renderedComment{{Author: "bob", BodyHTML: template.HTML("<p>try</p>")}}}},
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// A suggestion renders as the lines it replaces and the ones it proposes,
// with a button for someone who can push to the source branch.
func TestSuggestionRendersAsDiff(t *testing.T) {
	sg := &control.SuggestionOut{StartLine: 4, EndLine: 5, Original: "old a\r\nold b\r\n",
		Replacement: "new a\n", Apply: "server"}
	out := renderSuggestion(t, sg, true)
	for _, w := range []string{
		`<tr class="del"><td class="ln">4</td><td class="src">old a</td>`,
		`<tr class="del"><td class="ln">5</td><td class="src">old b</td>`,
		`<tr class="add"><td class="ln">4</td><td class="src">new a</td>`,
		`action="/krz/gitbay/mrs/42/suggestion"`, `name="thread" value="7"`, "Apply suggestion",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("page lacks %q", w)
		}
	}
	if out := renderSuggestion(t, sg, false); strings.Contains(out, "Apply suggestion") {
		t.Error("apply button shown to someone who cannot push to the source branch")
	}
}

// Where the server cannot sign, the page gives the command; an outdated
// suggestion says why and offers neither.
func TestSuggestionLocalAndOutdated(t *testing.T) {
	local := renderSuggestion(t, &control.SuggestionOut{StartLine: 1, EndLine: 1, Original: "a\n",
		Replacement: "b\n", Apply: "local"}, true)
	if !strings.Contains(local, "<code>gitbay mr apply-suggestion alice/app 1 7</code>") || strings.Contains(local, "Apply suggestion</button>") {
		t.Error("require-signed suggestion does not give the CLI command in place of the button")
	}
	stale := renderSuggestion(t, &control.SuggestionOut{StartLine: 1, EndLine: 1, Original: "a\n",
		Replacement: "b\n", Apply: "server", Outdated: true, Reason: "the lines it replaces have changed"}, true)
	if !strings.Contains(stale, "outdated suggestion: the lines it replaces have changed") || strings.Contains(stale, "Apply suggestion</button>") {
		t.Error("outdated suggestion is not marked, or still offers the button")
	}
}

// The thread body renders without the raw block the diff stands in for.
func TestAttachThreadsStripsSuggestionBlock(t *testing.T) {
	md := func(src, _ string) template.HTML { return template.HTML(src) }
	cm := store.DiffComment{ID: 3, Author: "bob", HeadSHA: "h", Path: "a.go", Side: "new", Line: 2,
		Body: "try this\n```suggestion\nx\n```\n"}
	view := &suggestionView{}
	_, detached := attachThreads(nil, []store.DiffComment{cm}, "h", md, reviewRights{},
		map[int64]*suggestionView{3: view})
	if len(detached) != 1 || detached[0].Suggestion != view || string(detached[0].Comments[0].BodyHTML) != "try this" {
		t.Fatalf("thread = %+v", detached)
	}
}
