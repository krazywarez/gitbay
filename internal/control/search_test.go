package control

import (
	"bytes"
	"testing"
)

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
