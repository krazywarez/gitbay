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
	builds := []control.BuildOut{{
		Number: 60, Job: "build", Status: "success",
		SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b",
		Ref: "cli-coverage", CreatedAt: "2026-08-28T04:42:54Z",
	}}
	jobs := []control.JobOut{{Name: "build"}, {Name: "nightly", Schedule: "0 3 * * *"}}
	filter := buildFilter{}
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		Runs     []buildRun
		Filter   buildFilter
		Facets   []facetGroup
		Refs     []string
		Older    string
		CanWrite bool
		Notice   string
	}{
		testRepoPage(), builds, jobs, groupRuns(builds), filter, nil,
		distinctRefs(builds, filter.Ref), "", true, "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"build", "success", "cli-coverage", "ff6271a9d4",
		`value="build"`, `value="nightly"`, "schedule 0 3 * * *",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("builds.html missing %q", want)
		}
	}
}

func TestBuildPageRendersCommandOutput(t *testing.T) {
	var sb strings.Builder
	err := web.Render(&sb, "build.html", buildView{
		repoPage: testRepoPage(),
		Build: control.BuildOut{
			Number: 60, Job: "build", Status: "success",
			SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b", Ref: "cli-coverage",
			CreatedAt: "2026-08-28T04:42:54Z", FinishedAt: "2026-08-28T04:43:06Z",
		},
		Log:      "step 1 ok",
		CanWrite: true,
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
	// A finished build offers no cancel control, even to a writer.
	if strings.Contains(out, "/cancel") {
		t.Error("build.html offers cancel on a finished build")
	}
}

// The count under the heading said "15 runs" while listing 23 builds
// (#240). It names both numbers now, so neither is mistaken for the other.
func TestBuildsPageCountsBuildsAndRuns(t *testing.T) {
	builds := []control.BuildOut{
		{Number: 4, Job: "instances", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "2026-09-20T06:00:00Z"},
		{Number: 3, Job: "instances", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "2026-09-19T06:00:00Z"},
		{Number: 2, Job: "lint", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "2026-09-12T11:20:03Z"},
		{Number: 1, Job: "unit", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "2026-09-12T11:20:03Z"},
	}
	var sb strings.Builder
	filter := buildFilter{}
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		Runs     []buildRun
		Filter   buildFilter
		Facets   []facetGroup
		Refs     []string
		Older    string
		CanWrite bool
		Notice   string
	}{
		testRepoPage(), builds, nil, groupRuns(builds), filter, nil,
		distinctRefs(builds, filter.Ref), "", true, "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sb.String(), "4 builds in 3 runs") {
		t.Errorf("builds.html count line: %q", sb.String())
	}
}

// A run row led with a ten-character sha and nothing said what the commit
// was (#241). The subject leads now, the sha follows as metadata, and a
// build whose commit is gone falls back to the sha alone.
func TestBuildsPageLeadsWithTheSubject(t *testing.T) {
	builds := []control.BuildOut{
		{Number: 2, Job: "unit", Status: "success", SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b",
			Ref: "main", CreatedAt: "2026-09-20T06:00:00Z", Subject: "runner: cap a build's container"},
		{Number: 1, Job: "unit", Status: "failure", SHA: "aa11bb22cc33dd44ee55ff6677889900aabbccdd",
			Ref: "main", CreatedAt: "2026-09-19T06:00:00Z"},
	}
	out := renderBuilds(t, builds, buildFilter{}, "?cursor=abc")
	if !strings.Contains(out, "runner: cap a build&#39;s container") {
		t.Errorf("builds.html does not lead with the subject:\n%s", out)
	}
	if !strings.Contains(out, "<code>ff6271a9d4</code>") {
		t.Errorf("builds.html drops the sha from the metadata line:\n%s", out)
	}
	// The commit is gone, so the sha is all there is to name the row by.
	if !strings.Contains(out, ">aa11bb22cc</a>") {
		t.Errorf("a subjectless run does not fall back to its sha:\n%s", out)
	}
}

// Filtering appeared to raise the run count because the list was capped
// at 50 with the window unstated (#244). The page is paged now: the count
// says it counts this page, and the link to the next one carries every
// filter.
func TestBuildsPagePagesAndSaysSo(t *testing.T) {
	builds := []control.BuildOut{{Number: 1, Job: "unit", Status: "failure", SHA: "aaa", Ref: "main", CreatedAt: "2026-09-19T06:00:00Z"}}
	out := renderBuilds(t, builds, buildFilter{Status: "failure"}, "?cursor=abc&status=failure")
	if !strings.Contains(out, "1 build in 1 run on this page") {
		t.Errorf("count line does not name the page:\n%s", out)
	}
	if !strings.Contains(out, `href="?cursor=abc&amp;status=failure"`) {
		t.Errorf("pager link missing or drops the filter:\n%s", out)
	}
	// With everything on one page the count is the whole count.
	if out := renderBuilds(t, builds, buildFilter{}, ""); strings.Contains(out, "on this page") {
		t.Errorf("an unpaged listing still hedges the count:\n%s", out)
	}
}

func TestOlderBuildsCarriesFilters(t *testing.T) {
	if got := olderBuilds(buildFilter{Ref: "main"}, ""); got != "" {
		t.Errorf("no next cursor should mean no link, got %q", got)
	}
	got := olderBuilds(buildFilter{Ref: "feature/x", Status: "failure", Job: "unit"}, "c1")
	for _, want := range []string{"cursor=c1", "ref=feature%2Fx", "status=failure", "job=unit"} {
		if !strings.Contains(got, want) {
			t.Errorf("olderBuilds = %q, missing %q", got, want)
		}
	}
}

func renderBuilds(t *testing.T, builds []control.BuildOut, filter buildFilter, older string) string {
	t.Helper()
	var sb strings.Builder
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		Runs     []buildRun
		Filter   buildFilter
		Facets   []facetGroup
		Refs     []string
		Older    string
		CanWrite bool
		Notice   string
	}{
		testRepoPage(), builds, nil, groupRuns(builds), filter, nil,
		distinctRefs(builds, filter.Ref), older, true, "",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}
