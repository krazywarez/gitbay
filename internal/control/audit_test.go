package control

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"30m":                  now.Add(-30 * time.Minute),
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"2026-08-01":           time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		"2026-08-01T10:00:00Z": time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, ok := parseSince(in, now)
		if !ok || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "-1h", "x d", "2026-13-01"} {
		if _, ok := parseSince(bad, now); ok {
			t.Errorf("parseSince(%q) accepted", bad)
		}
	}
}

// TestAuditArgsDropFlagValues: prose reaches argv through --body and
// --message, and the audit log kept it verbatim for a repository that may
// be private. Identifiers are positional and survive; flag names survive
// so the entry still says what shape the command had (#122).
func TestAuditArgsDropFlagValues(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"krz/gitbay", "--title", "a bug", "--body", "the whole issue text"},
			[]string{"krz/gitbay", "--title", "--body"}},
		{[]string{"krz/gitbay", "7", "--add", "bug", "--add", "ui"},
			[]string{"krz/gitbay", "7", "--add", "--add"}},
		// A switch takes no value, so the next argument is not swallowed.
		{[]string{"krz/gitbay", "--private", "--name", "x"},
			[]string{"krz/gitbay", "--private", "--name"}},
		// After "--" everything is positional, including a token that
		// looks like a flag.
		{[]string{"krz/gitbay", "--", "--not-a-flag"},
			[]string{"krz/gitbay", "--", "--not-a-flag"}},
		{nil, []string{}},
	}
	for _, tc := range cases {
		got := auditArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("auditArgs(%v) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("auditArgs(%v) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
