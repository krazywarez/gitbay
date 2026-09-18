package httpd

import (
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// D04: build events on the same commit fold into one feed line, a "run",
// whose State is the worst of the folded jobs' outcomes.
func TestFeedLinesFoldsBuildRunsBySHA(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":1,"job":"unit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.failure",
			Data: `{"number":2,"job":"lint","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
	}
	lines := feedLines(events)
	if len(lines) != 1 {
		t.Fatalf("feedLines returned %d lines, want 1: %+v", len(lines), lines)
	}
	l := lines[0]
	if l.State != "failure" {
		t.Errorf("State = %q, want failure", l.State)
	}
	if l.Verb != "ran 2 jobs on" {
		t.Errorf("Verb = %q, want %q", l.Verb, "ran 2 jobs on")
	}
	if l.Ref != "aaaaaaaaaa" {
		t.Errorf("Ref = %q, want short sha", l.Ref)
	}
	if l.URL != "/alice/app/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("URL = %q", l.URL)
	}
	if len(l.Jobs) != 2 || l.Jobs[0] != "unit" || l.Jobs[1] != "lint" {
		t.Errorf("Jobs = %+v", l.Jobs)
	}
}

// A single build event with a sha still gets a State, and keeps its
// ordinary verb/ref/url — a run of one job reads the same as before.
func TestFeedLinesSingleBuildGetsState(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":1,"job":"unit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
	}
	lines := feedLines(events)
	if len(lines) != 1 {
		t.Fatalf("feedLines returned %d lines, want 1", len(lines))
	}
	l := lines[0]
	if l.State != "success" {
		t.Errorf("State = %q, want success", l.State)
	}
	if l.Verb != "build success" || l.Ref != "unit" || l.URL != "/alice/app/builds/1" {
		t.Errorf("single build line changed shape: %+v", l)
	}
}

// Two different commits never fold, even back to back.
func TestFeedLinesDoesNotFoldAcrossDifferentSHAs(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":1,"job":"unit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":2,"job":"lint","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`},
	}
	lines := feedLines(events)
	if len(lines) != 2 {
		t.Fatalf("feedLines returned %d lines, want 2: %+v", len(lines), lines)
	}
}

// An event with no sha (an older event recorded before this field existed)
// never folds into anything, even when it shares a repo with an adjacent
// build event.
func TestFeedLinesNoSHANeverFolds(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success", Data: `{"number":1,"job":"unit"}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success", Data: `{"number":2,"job":"lint"}`},
	}
	lines := feedLines(events)
	if len(lines) != 2 {
		t.Fatalf("feedLines returned %d lines, want 2: %+v", len(lines), lines)
	}
	if lines[0].State != "" || lines[1].State != "" {
		t.Errorf("shaless build lines got a State: %+v", lines)
	}
}

// A non-build event between two builds of the same commit breaks the
// fold: only adjacent build events on the same commit combine.
func TestFeedLinesNonBuildEventBreaksFold(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":1,"job":"unit","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{RepoPath: "alice/app", Actor: "bob", Kind: "issue.created", Data: `{"number":1}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.failure",
			Data: `{"number":2,"job":"lint","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
	}
	lines := feedLines(events)
	if len(lines) != 3 {
		t.Fatalf("feedLines returned %d lines, want 3: %+v", len(lines), lines)
	}
}

// worstStatus governs the run's combined State the same way it governs
// combinedStatus for the builds tab.
func TestFeedLinesRunStatePrecedence(t *testing.T) {
	cases := []struct {
		statuses []string
		want     string
	}{
		{[]string{"success", "success"}, "success"},
		{[]string{"success", "pending"}, "pending"},
		{[]string{"pending", "running"}, "running"},
		{[]string{"running", "cancelled"}, "cancelled"},
		{[]string{"cancelled", "failure"}, "failure"},
	}
	for _, tc := range cases {
		events := make([]store.FeedEvent, len(tc.statuses))
		for i, s := range tc.statuses {
			events[i] = store.FeedEvent{RepoPath: "alice/app", Actor: "alice", Kind: "build." + s,
				Data: `{"number":1,"job":"j","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`}
		}
		lines := feedLines(events)
		if len(lines) != 1 || lines[0].State != tc.want {
			t.Errorf("statuses %v: got %+v, want State %q", tc.statuses, lines, tc.want)
		}
	}
}
