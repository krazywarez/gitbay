package httpd

import (
	"testing"
	"time"
)

// TestActivityGridMonthLabels checks the month label activityGrid attaches
// to each week: at most one label per calendar month, always on the week
// whose first non-pad day falls in the first 7 days of that month, and
// never the same month as the immediately preceding label.
func TestActivityGridMonthLabels(t *testing.T) {
	weeks, _ := activityGrid(nil)

	labelled := 0
	prev := ""
	for _, w := range weeks {
		if w.Month == "" {
			continue
		}
		labelled++
		if w.Month == prev {
			t.Fatalf("consecutive labelled weeks repeat month %q", w.Month)
		}
		prev = w.Month

		var first activityDay
		found := false
		for _, d := range w.Days {
			if !d.Pad {
				first = d
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("week labelled %q has no non-pad day", w.Month)
		}
		day, err := time.Parse("2006-01-02", first.Date)
		if err != nil {
			t.Fatalf("bad date %q: %v", first.Date, err)
		}
		if day.Day() > 7 {
			t.Fatalf("week labelled %q but first day is day %d of the month", w.Month, day.Day())
		}
		if got := day.Month().String()[:3]; got != w.Month {
			t.Fatalf("label %q does not match month %q of first day", w.Month, got)
		}
	}
	if labelled == 0 {
		t.Fatal("expected at least one month label across 53 weeks")
	}
}
