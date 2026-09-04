package store

import (
	"strings"
	"testing"
)

// queryPlan returns EXPLAIN QUERY PLAN for q as one string.
func queryPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.DB.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail)
		b.WriteString("\n")
	}
	return b.String()
}

// TestDashboardQueriesUseIndexes guards the plans the 0035 indexes exist
// for. Reachability is correlated subqueries and cannot be indexed, so
// what these queries buy from an index is the ORDER BY: the walk stops at
// LIMIT rather than sorting the table. A rewrite that reintroduces a sort
// puts the cost back — 0.8ms to 11.8ms on 20k issues — with no other
// symptom, which is what this asserts against (#137).
func TestDashboardQueriesUseIndexes(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	// The planner picks against an empty table the same way it does
	// against a full one here: these plans are driven by the ORDER BY and
	// the index's presence, not by row counts.
	cases := []struct {
		name string
		plan string
		want string
		// ordered marks the queries whose index supplies the ORDER BY, so
		// a sort in the plan means the walk is back to reading every row.
		// AssignedIssues is not one: it drives from one user's assignee
		// rows, a handful, and sorting those is the cheap half.
		ordered bool
	}{
		{"DashboardIssues", queryPlan(t, s, dashboardIssuesQuery, int64(1)), "issues_recent", true},
		{"DashboardMRs", queryPlan(t, s, dashboardMRsQuery, int64(1)), "merge_requests_recent", true},
		{"ReviewQueue", queryPlan(t, s, reviewQueueQuery, int64(1)), "merge_requests_recent", true},
		{"AssignedIssues", queryPlan(t, s, assignedIssuesQuery, int64(1)), "issue_assignees_user", false},
	}
	for _, tc := range cases {
		if !strings.Contains(tc.plan, tc.want) {
			t.Errorf("%s does not use %s:\n%s", tc.name, tc.want, tc.plan)
		}
		if tc.ordered && strings.Contains(tc.plan, "USE TEMP B-TREE FOR ORDER BY") {
			t.Errorf("%s sorts instead of walking an index:\n%s", tc.name, tc.plan)
		}
	}
}
