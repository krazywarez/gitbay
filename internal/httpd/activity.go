package httpd

import "time"

// activityDay is one cell of the graph; Level buckets Count into the five
// intensity classes the stylesheet colors.
type activityDay struct {
	Date  string
	Count int
	Level int // 0..4
	Pad   bool // before the range start / after today
}

type activityWeek []activityDay // 7 days, Sunday first

// activityGrid lays a day->count map into 53 week columns ending today,
// GitHub-style: columns are weeks, rows Sunday..Saturday.
func activityGrid(counts map[string]int) ([]activityWeek, int) {
	today := time.Now().UTC()
	// End the grid on the Saturday of the current week.
	end := today.AddDate(0, 0, int(time.Saturday-today.Weekday()))
	start := end.AddDate(0, 0, -53*7+1) // a Sunday, 53 columns back

	total := 0
	var weeks []activityWeek
	for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
		var week activityWeek
		for i := 0; i < 7; i++ {
			day := d.AddDate(0, 0, i)
			key := day.Format("2006-01-02")
			if day.After(today) {
				week = append(week, activityDay{Date: key, Pad: true})
				continue
			}
			n := counts[key]
			total += n
			week = append(week, activityDay{Date: key, Count: n, Level: activityLevel(n)})
		}
		weeks = append(weeks, week)
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

// activitySince is the first day the grid can show, for the query bound.
func activitySince() string {
	today := time.Now().UTC()
	end := today.AddDate(0, 0, int(time.Saturday-today.Weekday()))
	return end.AddDate(0, 0, -53*7+1).Format("2006-01-02")
}
