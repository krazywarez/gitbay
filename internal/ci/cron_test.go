package ci

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	cases := []struct{ expr, from, want string }{
		{"* * * * *", "2026-08-25 10:30", "2026-08-25 10:31"},
		{"17 11,23 * * *", "2026-08-25 10:30", "2026-08-25 11:17"},
		{"17 11,23 * * *", "2026-08-25 11:17", "2026-08-25 23:17"},
		{"0 6 * * 1", "2026-08-25 00:00", "2026-08-31 06:00"}, // next Monday
		{"*/15 * * * *", "2026-08-25 10:31", "2026-08-25 10:45"},
		{"0 0 1 * *", "2026-08-25 10:00", "2026-09-01 00:00"},
		{"30 4 1-7 * 0", "2026-08-25 10:00", "2026-08-30 04:30"}, // dom OR dow: Sunday wins
		{"0 12 29 2 *", "2026-03-01 00:00", "2028-02-29 12:00"},  // leap day beyond a year -> zero
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		got := cr.Next(at(c.from))
		if c.expr == "0 12 29 2 *" {
			if !got.IsZero() {
				t.Errorf("%q from %s: want zero, got %s", c.expr, c.from, got)
			}
			continue
		}
		if !got.Equal(at(c.want)) {
			t.Errorf("%q from %s: got %s, want %s", c.expr, c.from, got, c.want)
		}
	}
}

func TestCronParseErrors(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "a * * * *", "*/0 * * * *", "5-2 * * * *",
	} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("%q parsed without error", expr)
		}
	}
}
