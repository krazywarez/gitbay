package httpd

import (
	"fmt"
	"reflect"
	"testing"
	"time"

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
			// Distinct job names: a repeat would split the line (#240).
			events[i] = store.FeedEvent{RepoPath: "alice/app", Actor: "alice", Kind: "build." + s,
				Data: fmt.Sprintf(`{"number":1,"job":"j%d","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, i)}
		}
		lines := feedLines(events)
		if len(lines) != 1 || lines[0].State != tc.want {
			t.Errorf("statuses %v: got %+v, want State %q", tc.statuses, lines, tc.want)
		}
	}
}

// D05: feedLines parses the stored RFC3339 timestamp into WhenT for the
// template's relative-time rendering; an unparseable value leaves it zero
// rather than panicking or guessing.
func TestFeedLinesParsesWhenT(t *testing.T) {
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "issue.created",
			Data: `{"number":1}`, CreatedAt: "2026-09-10T12:00:00Z"},
		{RepoPath: "alice/app", Actor: "alice", Kind: "issue.created",
			Data: `{"number":2}`, CreatedAt: "not-a-time"},
	}
	lines := feedLines(events)
	want, _ := time.Parse(time.RFC3339Nano, "2026-09-10T12:00:00Z")
	if !lines[0].WhenT.Equal(want) {
		t.Errorf("WhenT = %v, want %v", lines[0].WhenT, want)
	}
	if !lines[1].WhenT.IsZero() {
		t.Errorf("WhenT for bad timestamp = %v, want zero", lines[1].WhenT)
	}
	// When is preserved for anything that still reads the raw string.
	if lines[0].When != "2026-09-10T12:00:00Z" {
		t.Errorf("When = %q", lines[0].When)
	}
}

// A scheduled job firing daily on an unchanged tip is a separate event
// each tick, not another job of one run (#240): a repeated job name starts
// a new line, so three days read as three lines rather than "ran 3 jobs on"
// one commit.
func TestFeedLinesSplitsRepeatedJob(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var events []store.FeedEvent
	for _, status := range []string{"success", "failure", "success"} {
		events = append(events, store.FeedEvent{RepoPath: "alice/app", Actor: "alice",
			Kind: "build." + status, Data: `{"number":1,"job":"instances","sha":"` + sha + `"}`})
	}
	lines := feedLines(events)
	if len(lines) != 3 {
		t.Fatalf("feedLines returned %d lines, want 3: %+v", len(lines), lines)
	}
	for i, want := range []string{"success", "failure", "success"} {
		if lines[i].Verb != "build "+want || lines[i].Ref != "instances" {
			t.Errorf("line %d: %+v, want Verb %q on job instances", i, lines[i], "build "+want)
		}
	}
}

// What the job-name rule cannot do, documented so the limit is not
// rediscovered as a bug: these events are recorded per job at finish time,
// so the oldest scheduled line on a commit folds in the push's jobs. The
// builds tab does not have this problem — groupRuns has created_at.
func TestFeedLinesScheduleAbsorbsPushJobs(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	events := []store.FeedEvent{
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":3,"job":"instances","sha":"` + sha + `"}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":2,"job":"instances","sha":"` + sha + `"}`},
		{RepoPath: "alice/app", Actor: "alice", Kind: "build.success",
			Data: `{"number":1,"job":"lint","sha":"` + sha + `"}`},
	}
	lines := feedLines(events)
	if len(lines) != 2 {
		t.Fatalf("feedLines returned %d lines, want 2: %+v", len(lines), lines)
	}
	if !reflect.DeepEqual(lines[1].Jobs, []string{"instances", "lint"}) {
		t.Errorf("second line jobs: %+v, want the schedule and the push folded", lines[1].Jobs)
	}
}
