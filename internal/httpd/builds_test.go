package httpd

import (
	"fmt"
	"reflect"
	"testing"

	"gitbay.org/gitbay/internal/control"
)

// groupRuns folds consecutive same-commit builds (the list is newest
// first, so a commit's jobs are adjacent) into one run per commit, and
// gives the run a combined status: worst first (failure beats everything,
// then cancelled, running, pending), success only when every job is (#224).
func TestGroupRunsCombinesByCommit(t *testing.T) {
	builds := []control.BuildOut{
		{Number: 3, Job: "lint", Status: "success", SHA: "bbb", Ref: "main", CreatedAt: "t2"},
		{Number: 2, Job: "unit", Status: "failure", SHA: "aaa", Ref: "main", CreatedAt: "t1"},
		{Number: 1, Job: "lint", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "t1"},
	}
	runs := groupRuns(builds)
	if len(runs) != 2 {
		t.Fatalf("groupRuns returned %d runs, want 2: %+v", len(runs), runs)
	}
	if runs[0].SHA != "bbb" || len(runs[0].Builds) != 1 || runs[0].Status != "success" {
		t.Errorf("first run: %+v", runs[0])
	}
	if runs[1].SHA != "aaa" || len(runs[1].Builds) != 2 || runs[1].Status != "failure" {
		t.Errorf("second run: %+v", runs[1])
	}
	// Order within a run is preserved from the input.
	if runs[1].Builds[0].Job != "unit" || runs[1].Builds[1].Job != "lint" {
		t.Errorf("run builds out of order: %+v", runs[1].Builds)
	}
}

func TestGroupRunsEmpty(t *testing.T) {
	if runs := groupRuns(nil); len(runs) != 0 {
		t.Errorf("groupRuns(nil) = %+v, want empty", runs)
	}
}

// Two builds on the same sha but on different refs (a fast-forward merge
// can leave the commit reachable from more than one branch) are not
// adjacent unless the list happens to put them there; groupRuns only folds
// what is actually adjacent, so this documents that a same-sha, same-ref
// pair from one push is what gets folded, not "any build of this sha ever".
func TestGroupRunsCombinedStatusPriority(t *testing.T) {
	cases := []struct {
		statuses []string
		want     string
	}{
		{[]string{"success"}, "success"},
		{[]string{"success", "pending"}, "pending"},
		{[]string{"pending", "running"}, "running"},
		{[]string{"running", "cancelled"}, "cancelled"},
		{[]string{"cancelled", "failure"}, "failure"},
		{[]string{"success", "success", "failure"}, "failure"},
		// A status outside runStatusPriority (a future state such as
		// "skipped") is still not success: it must not fall through to
		// the "success" default and read as green.
		{[]string{"success", "skipped"}, "skipped"},
	}
	for _, tc := range cases {
		var builds []control.BuildOut
		for _, s := range tc.statuses {
			builds = append(builds, control.BuildOut{SHA: "x", Status: s})
		}
		runs := groupRuns(builds)
		if len(runs) != 1 || runs[0].Status != tc.want {
			t.Errorf("statuses %v: combined %+v, want %q", tc.statuses, runs, tc.want)
		}
	}
}

// filterLinks builds the nav.filters row: one link that clears both status
// and job, one per known status and one per known job, each preserving the
// other two query parameters and marking itself active (#224).
func TestFilterLinksPreservesOtherParamsAndMarksActive(t *testing.T) {
	links := filterLinks(buildFilter{Ref: "main", Status: "success", Job: "lint"},
		[]control.JobOut{{Name: "lint"}, {Name: "unit"}})

	byLabel := map[string]buildFilterLink{}
	for _, l := range links {
		byLabel[l.Label] = l
	}
	all, ok := byLabel["all"]
	if !ok {
		t.Fatal("no \"all\" link")
	}
	if all.Active {
		t.Error(`"all" is active while a status/job filter is set`)
	}
	if all.Href != "?ref=main" {
		t.Errorf(`"all" href = %q, want "?ref=main" (clears status and job, keeps ref)`, all.Href)
	}

	success, ok := byLabel["success"]
	if !ok || !success.Active {
		t.Errorf("success link: %+v, want present and active", success)
	}
	if success.Href != "?job=lint&ref=main&status=success" {
		t.Errorf("success href = %q", success.Href)
	}

	lint, ok := byLabel["lint"]
	if !ok || !lint.Active {
		t.Errorf("lint link: %+v, want present and active", lint)
	}
	if lint.Href != "?job=lint&ref=main&status=success" {
		t.Errorf("lint href = %q", lint.Href)
	}

	unit, ok := byLabel["unit"]
	if !ok || unit.Active {
		t.Errorf("unit link: %+v, want present and inactive", unit)
	}
	if unit.Href != "?job=unit&ref=main&status=success" {
		t.Errorf("unit href = %q", unit.Href)
	}
}

// With no filter at all, "all" is the active link.
func TestFilterLinksAllActiveWhenUnfiltered(t *testing.T) {
	links := filterLinks(buildFilter{}, nil)
	for _, l := range links {
		if l.Label == "all" && !l.Active {
			t.Error(`"all" is not active with no filter set`)
		}
	}
}

// distinctRefs lists each ref once, in the order builds carry them, and
// always includes the current filter value even if it matched nothing —
// it powers the branch field's suggestions, not a strict "what exists" list.
func TestDistinctRefsDedupesAndIncludesCurrent(t *testing.T) {
	builds := []control.BuildOut{{Ref: "main"}, {Ref: "feature"}, {Ref: "main"}}
	got := distinctRefs(builds, "release")
	want := []string{"main", "feature", "release"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distinctRefs = %v, want %v", got, want)
	}
}

func TestDistinctRefsNoDuplicateWhenCurrentAlreadyPresent(t *testing.T) {
	builds := []control.BuildOut{{Ref: "main"}}
	got := distinctRefs(builds, "main")
	want := []string{"main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distinctRefs = %v, want %v", got, want)
	}
}

// The builds column groups the same links filterLinks makes: "all" and
// the statuses, then the jobs, then the branches seen (desktop layout spec).
func TestBuildFacetsGroups(t *testing.T) {
	f := buildFilter{Ref: "main", Status: "success"}
	groups := buildFacets(f, []control.JobOut{{Name: "lint"}}, []string{"main", "dev"})
	if len(groups) != 3 || groups[0].Title != "Status" || groups[1].Title != "Jobs" || groups[2].Title != "Branches" {
		t.Fatalf("groups: %+v", groups)
	}
	if groups[0].Items[0].Label != "all" || groups[0].Items[0].Href != "?ref=main" || groups[0].Items[0].Active {
		t.Errorf("all: %+v", groups[0].Items[0])
	}
	if s := groups[0].Items[3]; s.Label != "success" || !s.Active {
		t.Errorf("success: %+v", s)
	}
	if j := groups[1].Items[0]; j.Label != "lint" || j.Href != "?job=lint&ref=main&status=success" || j.Active {
		t.Errorf("lint: %+v", j)
	}
	if b := groups[2].Items[0]; b.Label != "main" || !b.Active || b.Href != "?status=success" {
		t.Errorf("active branch clears itself: %+v", b)
	}
	if b := groups[2].Items[1]; b.Label != "dev" || b.Active || b.Href != "?ref=dev&status=success" {
		t.Errorf("dev: %+v", b)
	}
}

// The branch group caps, the way topicFacets caps topics: a page of
// builds can name dozens of refs and the column is not a branch
// listing. The ref in force is kept whatever its position (#237).
func TestBuildFacetsCapsBranches(t *testing.T) {
	var refs []string
	for i := 0; i < 30; i++ {
		refs = append(refs, fmt.Sprintf("b%02d", i))
	}
	groups := buildFacets(buildFilter{}, nil, refs)
	branches := groups[2].Items
	if len(branches) != maxBranchFacets {
		t.Fatalf("branches: %d", len(branches))
	}
	if branches[0].Label != "b00" || branches[len(branches)-1].Label != "b09" {
		t.Errorf("kept the wrong refs: %+v", branches)
	}

	groups = buildFacets(buildFilter{Ref: "b29"}, nil, refs)
	branches = groups[2].Items
	if len(branches) != maxBranchFacets {
		t.Fatalf("branches with an active ref: %d", len(branches))
	}
	var active *facetItem
	for i := range branches {
		if branches[i].Label == "b29" {
			active = &branches[i]
		}
	}
	if active == nil || !active.Active || active.Href != "?" {
		t.Errorf("active ref past the cap: %+v", branches)
	}
}

// A scheduled job runs against the same sha every tick for as long as the
// branch tip does not move, so the sha alone is not the run (#240). Three
// daily runs of "instances" on one commit are three rows, each with its own
// timestamp and status; the push that set the tip queued lint and unit
// together, so those stay one row.
func TestGroupRunsSeparatesRepeatedSchedule(t *testing.T) {
	builds := []control.BuildOut{
		{Number: 5, Job: "instances", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "t5"},
		{Number: 4, Job: "instances", Status: "failure", SHA: "aaa", Ref: "main", CreatedAt: "t4"},
		{Number: 3, Job: "instances", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "t3"},
		{Number: 2, Job: "lint", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "t2"},
		{Number: 1, Job: "unit", Status: "success", SHA: "aaa", Ref: "main", CreatedAt: "t2"},
	}
	runs := groupRuns(builds)
	if len(runs) != 4 {
		t.Fatalf("groupRuns returned %d runs, want 4: %+v", len(runs), runs)
	}
	for i, want := range []struct {
		when   string
		status string
		jobs   int
	}{{"t5", "success", 1}, {"t4", "failure", 1}, {"t3", "success", 1}, {"t2", "success", 2}} {
		got := runs[i]
		if got.CreatedAt != want.when || got.Status != want.status || len(got.Builds) != want.jobs {
			t.Errorf("run %d: %+v, want CreatedAt %q status %q with %d builds",
				i, got, want.when, want.status, want.jobs)
		}
	}
}
