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
