package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

// The scope sentence is a permanent caption, not a first-visit-only
// hint: a visitor who has already searched still needs to know what a
// search here does and does not cover (#270).
func TestGlobalSearchScopeCaptionAlwaysShown(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "globalsearch.html", struct {
		basePage
		Tab      string
		Query    string
		Kind     string
		QueryErr string
		Results  []searchResult
	}{basePage{Site: "gitbay"}, "sitesearch", "gitbay", "", "", nil}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sb.String(), "File contents are searched per repository") {
		t.Error("scope caption missing once a query is present")
	}
}
