package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/web"
)

// The settings page renders what `repo deps status` reports, not just the
// toggle (#164). A renamed field would be a blank cell rather than a
// compile error, so render the page and look for the values.
func TestSettingsPageRendersDepsStatus(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "settings.html", settingsPage{
		repoPage:    testRepoPage(),
		DepsEnabled: true,
		Deps: control.DepsOut{
			Enabled: true, LastCheck: "2026-09-03T08:20:25Z", IssueNumber: 140,
			Behind: []control.DepBehind{{Ecosystem: "go", Name: "golang.org/x/crypto",
				Current: "v0.31.0", Latest: "v0.42.0"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := sb.String()
	for _, want := range []string{
		"2026-09-03 08:20 UTC", "golang.org/x/crypto", "v0.31.0", "v0.42.0",
		`href="/krz/gitbay/issues/140"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page does not show %q", want)
		}
	}
}

// With checks off, none of the report shows.
func TestSettingsPageHidesDepsStatusWhenOff(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "settings.html", settingsPage{repoPage: testRepoPage()}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "Last checked") {
		t.Error("the report shows with checks off")
	}
}
