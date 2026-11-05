package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/codescan"
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

// The code scanning section offers each open finding for dismissal, each
// dismissed one for reopening, and says what the scan set aside.
func TestSettingsPageRendersSecurityTriage(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "settings.html", settingsPage{
		repoPage: testRepoPage(),
		Security: control.SecurityOut{
			Enabled: true, LastResults: "2026-11-01T04:02:00Z",
			Findings:  []control.SecurityFinding{{Fingerprint: "aaaa1111bbbb", Rule: "go.lang.x", Path: "a.go", Line: 3}},
			Dismissed: []control.SecurityFinding{{Fingerprint: "cccc2222dddd", Rule: "go.lang.y", Path: "b.go", Line: 9, Reason: "test", DismissedBy: "alice"}},
			Aside:     codescan.Aside{Unused: map[string]int{"django": 26}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := sb.String()
	for _, want := range []string{
		`value="security-dismiss"`, `value="aaaa1111bbbb"`, `value="security-reopen"`, `value="cccc2222dddd"`,
		"by alice", "26 from django rules", `name="security-ignore"`, `name="security-exclude-rule"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page does not show %q", want)
		}
	}
}

// The frequency dropdown offers what the instance allows, with the
// repository's frequency selected.
func TestSettingsPageRendersScanFrequency(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "settings.html", settingsPage{
		repoPage: testRepoPage(), ScanAvailable: true,
		ScanFrequencies: []string{"weekly", "monthly"}, ScanFrequency: "weekly", ScanHour: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := sb.String()
	for _, want := range []string{
		`value="security-frequency"`, `<option value="weekly" selected>weekly</option>`,
		`<option value="monthly">monthly</option>`, "04:00 UTC",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page does not show %q", want)
		}
	}
	if strings.Contains(page, `value="daily"`) {
		t.Error("offers a frequency the instance does not allow")
	}
}

// The advisories offer dismissal, the dismissed ones reopening, and the
// ones nothing imports are named.
func TestSettingsPageRendersAdvisoryTriage(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "settings.html", settingsPage{
		repoPage:    testRepoPage(),
		DepsEnabled: true,
		Deps: control.DepsOut{
			Enabled: true, LastCheck: "2026-11-01T04:02:00Z",
			Advisories:  []control.DepAdvisory{{Ecosystem: "go", Name: "golang.org/x/net", Version: "v0.1.0", ID: "GO-2026-1"}},
			Dismissed:   []control.DepAdvisory{{Ecosystem: "npm", Name: "undici", ID: "GHSA-x", Reason: "dev-only", By: "alice"}},
			NotImported: []control.DepAdvisory{{Ecosystem: "go", Name: "golang.org/x/crypto", ID: "GO-2026-5932"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := sb.String()
	for _, want := range []string{
		`value="deps-dismiss"`, `name="advisory" value="GO-2026-1"`, `value="deps-reopen"`, `value="GHSA-x"`,
		"dev-only", "by alice", "GO-2026-5932</a> (golang.org/x/crypto)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page does not show %q", want)
		}
	}
}
