package httpd

import (
	"html/template"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// issue close/reopen and mr close/draft allow the author as well as
// writers (authorOrWrite), so an author without write access sees those
// controls; review and merge stay with writers (#311).

func renderIssueFor(t *testing.T, canEdit, canWrite bool) string {
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "issue.html", struct {
		repoPage
		Issue       store.Issue
		BodyHTML    template.HTML
		Comments    []renderedComment
		CanEdit     bool
		CanWrite    bool
		Milestones  []store.Milestone
		Notice      string
		LabelColors map[string]template.CSS
		Draft       *draft
		Reactions   map[int64]reactionBar
	}{repoPage: testRepoPage(), Issue: store.Issue{Number: 2, Title: "test issue", Author: "cmc", State: "open"},
		CanEdit: canEdit, CanWrite: canWrite, Reactions: map[int64]reactionBar{0: {}}}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestIssueCloseShownToAuthorWithoutWrite(t *testing.T) {
	if !strings.Contains(renderIssueFor(t, true, false), "Close issue") {
		t.Error("the author cannot close their own issue from the web")
	}
	if strings.Contains(renderIssueFor(t, false, false), "Close issue") {
		t.Error("a reader who is not the author sees Close issue")
	}
}

func renderMRFor(t *testing.T, canEdit, canWrite bool) string {
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "mr.html", mrPageData{
		repoPage: testRepoPage(), MR: testMR("open"), View: "conversation",
		CanEdit: canEdit, CanWrite: canWrite,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestMRCloseShownToAuthorWithoutWrite(t *testing.T) {
	author := renderMRFor(t, true, false)
	if !strings.Contains(author, "Close without merging") {
		t.Error("the author cannot close their own merge request from the web")
	}
	if !strings.Contains(author, "Convert to draft") {
		t.Error("the author cannot convert their own merge request to a draft")
	}
	if strings.Contains(author, "Approve") || strings.Contains(author, ">Merge</button>") {
		t.Error("the author without write access sees review or merge controls")
	}
	if strings.Contains(renderMRFor(t, false, false), "Close without merging") {
		t.Error("a reader who is not the author sees Close without merging")
	}
}
