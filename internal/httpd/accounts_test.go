package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

// The registered page's web steps are a numbered list, and mention where
// the iOS app gets a token, so a new user is not left guessing (#264).
func TestRegisteredPageNumberedStepsAndTokenMention(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "registered.html", struct {
		basePage
		Username, Message, Host string
	}{Username: "alice", Host: "gitbay.org"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "<ol>") {
		t.Error("next steps are not a numbered list")
	}
	if !strings.Contains(out, "Settings → API tokens") {
		t.Error("no mention of Settings → API tokens for the iOS app")
	}
}
