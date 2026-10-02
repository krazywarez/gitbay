package control

import (
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestChecksMark(t *testing.T) {
	st := func(states ...string) []store.CommitStatus {
		var out []store.CommitStatus
		for _, s := range states {
			out = append(out, store.CommitStatus{State: s})
		}
		return out
	}
	cases := []struct {
		in        []store.CommitStatus
		text, sgr string
	}{
		{nil, "", ""},
		{st("success", "success", "skipped"), "✓ 3/3", sgrGreen},
		{st("success", "pending"), "◐ 1 pending", sgrDim},
		{st("success", "failure", "error", "pending"), "✗ 2 failed", sgrRed},
	}
	for _, tc := range cases {
		if got := checksMark(tc.in); got.s != tc.text || got.sgr != tc.sgr {
			t.Errorf("checksMark(%v) = %q %q, want %q %q", tc.in, got.s, got.sgr, tc.text, tc.sgr)
		}
	}
}

func TestReviewMark(t *testing.T) {
	counts := map[string]bool{"bob": true, "carol": true}
	mr := store.MR{Author: "alice"}
	rv := func(who, verdict string, stale bool) store.MRReview {
		return store.MRReview{Reviewer: who, Verdict: verdict, Stale: stale}
	}
	cases := []struct {
		name    string
		mr      store.MR
		reviews []store.MRReview
		want    string
	}{
		{"nothing", mr, nil, ""},
		{"approved", mr, []store.MRReview{rv("bob", "approve", false)}, "approved"},
		{"changes outrank", mr, []store.MRReview{rv("bob", "approve", false), rv("carol", "request_changes", false)}, "changes requested"},
		{"latest verdict wins", mr, []store.MRReview{rv("bob", "request_changes", false), rv("bob", "approve", false)}, "approved"},
		{"stale ignored", mr, []store.MRReview{rv("bob", "approve", true)}, ""},
		{"non-writer ignored", mr, []store.MRReview{rv("dave", "approve", false)}, ""},
		{"author ignored", store.MR{Author: "bob"}, []store.MRReview{rv("bob", "approve", false)}, ""},
		{"asked of viewer", store.MR{Author: "alice", ReviewRequests: []string{"me"}}, nil, "review requested"},
		{"asked of others", store.MR{Author: "alice", ReviewRequests: []string{"bob"}}, nil, "requested"},
	}
	for _, tc := range cases {
		if got := reviewMark(tc.mr, tc.reviews, counts, "me"); got.s != tc.want {
			t.Errorf("%s: reviewMark = %q, want %q", tc.name, got.s, tc.want)
		}
	}
}

func TestLabelsMark(t *testing.T) {
	if got := labelsMark([]string{"a", "b", "c", "d"}); got != "a, b, +2" {
		t.Errorf("labelsMark = %q", got)
	}
}

func TestFailingBuilds(t *testing.T) {
	builds := []DashboardBuild{
		{Repo: "a/x", Job: "test", Ref: "main", Status: "success"},
		{Repo: "a/x", Job: "test", Ref: "main", Status: "failure"}, // replaced by the newer success
		{Repo: "a/y", Job: "pull", Ref: "main", Status: "failure"},
	}
	if got := failingBuilds(builds); got != 1 {
		t.Errorf("failingBuilds = %d, want 1", got)
	}
}

func TestNeedsYou(t *testing.T) {
	term := Term{Cols: 80}
	if got := term.needsYou(DashboardOut{}); got != "Nothing waits on you." {
		t.Errorf("empty = %q", got)
	}
	d := DashboardOut{Reviews: make([]DashboardItem, 2), Unread: 1}
	if got := term.needsYou(d); got != "Needs you: 2 reviews requested, 1 unread notification" {
		t.Errorf("needsYou = %q", got)
	}
}

func TestStepState(t *testing.T) {
	cases := []struct {
		status    string
		failed, n int
		want      string
	}{
		{"success", 0, 1, "success"},
		{"running", 0, 1, ""},
		{"failure", 0, 1, ""},
		{"failure", 2, 1, "success"},
		{"failure", 2, 2, "failure"},
		{"failure", 2, 3, "skipped"},
	}
	for _, tc := range cases {
		if got := stepState(tc.status, tc.failed, tc.n); got != tc.want {
			t.Errorf("stepState(%q, %d, %d) = %q, want %q", tc.status, tc.failed, tc.n, got, tc.want)
		}
	}
}

func TestKeyValues(t *testing.T) {
	got := keyValues(`{"source":"SHA256:x","argv":["--untrusted","a b"],"n":3}`)
	if got != "argv=[--untrusted a b] n=3 source=SHA256:x" {
		t.Errorf("keyValues = %q", got)
	}
	if got := keyValues("not json"); got != "not json" {
		t.Errorf("non-JSON = %q", got)
	}
}
