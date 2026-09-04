package httpd

import (
	"gitbay.org/gitbay/internal/control"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

func testRepoPage() repoPage {
	var p repoPage
	p.Repo = store.Repo{OwnerName: "krz", Name: "gitbay", DefaultBranch: "main"}
	p.Host = "gitbay.org"
	return p
}

// The build pages read their data from the build commands' JSON now, not
// from store.Build. A renamed field would be a blank cell rather than a
// compile error, so render both pages and look for the values.
func TestBuildsPageRendersCommandOutput(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		CanWrite bool
		Notice   string
	}{
		testRepoPage(),
		[]control.BuildOut{{
			Number: 60, Job: "build", Status: "success",
			SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b",
			Ref: "cli-coverage", CreatedAt: "2026-08-28T04:42:54Z",
		}},
		[]control.JobOut{{Name: "build"}, {Name: "nightly", Schedule: "0 3 * * *"}},
		true, "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"#60 build", "success", "cli-coverage", "ff6271a9d4",
		`value="build"`, `value="nightly"`, "schedule 0 3 * * *",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("builds.html missing %q", want)
		}
	}
}

func TestBuildPageRendersCommandOutput(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "build.html", struct {
		repoPage
		Build control.BuildOut
		Log   string
	}{
		testRepoPage(),
		control.BuildOut{
			Number: 60, Job: "build", Status: "success",
			SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b", Ref: "cli-coverage",
			CreatedAt: "2026-08-28T04:42:54Z", FinishedAt: "2026-08-28T04:43:06Z",
		},
		"step 1 ok",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"Build 60", "success", "build on cli-coverage", "ff6271a9d4",
		"2026-08-28 04:42", "2026-08-28 04:43", "step 1 ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("build.html missing %q", want)
		}
	}
}
