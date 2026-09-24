package control

import (
	"bytes"
	"strings"
	"testing"
)

// A repo hit has no state, an issue/mr hit does. At a terminal both must
// still put TITLE under the TITLE header, not have the repo's title slide
// left under STATE.
func TestSearchTableAlignsTitleAtTerminal(t *testing.T) {
	results := []SearchResult{
		{Kind: "repo", Repo: "alice/webapp", Title: "a web application"},
		{Kind: "issue", Repo: "alice/webapp", Number: 4, Title: "memory leak", State: "open"},
	}
	var b bytes.Buffer
	writeSearchTable(&Ctx{Term: Term{Cols: 100}}, &b, results)

	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines:\n%s", len(lines), b.String())
	}
	header, repoRow, issueRow := lines[0], lines[1], lines[2]
	titleAt := strings.Index(header, "TITLE")
	repoTitleAt := strings.Index(repoRow, "a web application")
	issueTitleAt := strings.Index(issueRow, "memory leak")
	if titleAt < 0 || repoTitleAt < 0 || issueTitleAt < 0 {
		t.Fatalf("columns not found:\n%s", b.String())
	}
	if repoTitleAt != issueTitleAt {
		t.Errorf("titles not aligned: repo row at %d, issue row at %d\n%s", repoTitleAt, issueTitleAt, b.String())
	}
	if repoTitleAt != titleAt {
		t.Errorf("title not under TITLE header: header at %d, repo row at %d\n%s", titleAt, repoTitleAt, b.String())
	}
}

// Plain output has no header to align to, so a repo row stays 3 cells —
// bytes must not change from before the terminal fix.
func TestSearchTablePlainRepoRowIsThreeCells(t *testing.T) {
	results := []SearchResult{
		{Kind: "repo", Repo: "alice/webapp", Title: "a web application"},
		{Kind: "issue", Repo: "alice/webapp", Number: 4, Title: "memory leak", State: "open"},
	}
	var b bytes.Buffer
	writeSearchTable(&Ctx{}, &b, results)

	want := "repo\talice/webapp\ta web application\n" +
		"issue\talice/webapp#4\topen\tmemory leak\n"
	if b.String() != want {
		t.Errorf("plain:\n%q\nwant\n%q", b.String(), want)
	}
}
