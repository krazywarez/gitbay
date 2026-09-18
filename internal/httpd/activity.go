package httpd

import "time"

// activityDay is one cell of the graph; Level buckets Count into the five
// intensity classes the stylesheet colors.
type activityDay struct {
	Date  string
	Count int
	Level int  // 0..4
	Pad   bool // before the range start / after today
}

// activityWeek is one column of the graph: 7 days, Sunday first, plus the
// month label to show above it (empty for most weeks).
type activityWeek struct {
	Days  []activityDay
	Month string
}

// activityGrid lays a day->count map into 53 week columns ending today,
// GitHub-style: columns are weeks, rows Sunday..Saturday. Each week whose
// first non-pad day falls within the first 7 days of a month, and whose
// month differs from the last labelled week, carries that month's
// three-letter name.
func activityGrid(counts map[string]int) ([]activityWeek, int) {
	today := time.Now().UTC()
	// End the grid on the Saturday of the current week.
	end := today.AddDate(0, 0, int(time.Saturday-today.Weekday()))
	start := end.AddDate(0, 0, -53*7+1) // a Sunday, 53 columns back

	total := 0
	var weeks []activityWeek
	prevMonth := ""
	for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
		var days []activityDay
		var firstDay time.Time
		haveFirst := false
		for i := 0; i < 7; i++ {
			day := d.AddDate(0, 0, i)
			key := day.Format("2006-01-02")
			if day.After(today) {
				days = append(days, activityDay{Date: key, Pad: true})
				continue
			}
			if !haveFirst {
				firstDay = day
				haveFirst = true
			}
			n := counts[key]
			total += n
			days = append(days, activityDay{Date: key, Count: n, Level: activityLevel(n)})
		}
		month := ""
		if haveFirst && firstDay.Day() <= 7 {
			if name := firstDay.Month().String()[:3]; name != prevMonth {
				month = name
				prevMonth = name
			}
		}
		weeks = append(weeks, activityWeek{Days: days, Month: month})
	}
	return weeks, total
}

func activityLevel(n int) int {
	switch {
	case n == 0:
		return 0
	case n <= 2:
		return 1
	case n <= 5:
		return 2
	case n <= 9:
		return 3
	default:
		return 4
	}
}
