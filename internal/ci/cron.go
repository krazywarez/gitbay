package ci

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field expression: minute, hour, day-of-month,
// month, day-of-week. Supported per field: "*", N, A-B, */N, A-B/N, and
// comma lists. Day-of-month and day-of-week combine with OR when both are
// restricted, per traditional cron.
type Cron struct {
	min, hour, dom, mon, dow map[int]bool
	domAny, dowAny           bool
}

// ParseCron validates and compiles an expression like "17 11,23 * * *".
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("cron %q: want 5 fields (min hour dom mon dow), got %d", expr, len(fields))
	}
	specs := []struct {
		lo, hi int
	}{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	var sets [5]map[int]bool
	for i, f := range fields {
		set, err := parseField(f, specs[i].lo, specs[i].hi)
		if err != nil {
			return Cron{}, fmt.Errorf("cron %q field %d: %w", expr, i+1, err)
		}
		sets[i] = set
	}
	// dow 7 is Sunday, same as 0.
	if sets[4][7] {
		sets[4][0] = true
	}
	return Cron{
		min: sets[0], hour: sets[1], dom: sets[2], mon: sets[3], dow: sets[4],
		domAny: fields[2] == "*", dowAny: fields[4] == "*",
	}, nil
}

func parseField(f string, lo, hi int) (map[int]bool, error) {
	set := map[int]bool{}
	for _, part := range strings.Split(f, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			s, err := strconv.Atoi(stepPart)
			if err != nil || s < 1 {
				return nil, fmt.Errorf("bad step %q", stepPart)
			}
			step = s
		}
		a, b := lo, hi
		if rangePart != "*" {
			loStr, hiStr, isRange := strings.Cut(rangePart, "-")
			n, err := strconv.Atoi(loStr)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", loStr)
			}
			a = n
			if isRange {
				m, err := strconv.Atoi(hiStr)
				if err != nil {
					return nil, fmt.Errorf("bad value %q", hiStr)
				}
				b = m
			} else if hasStep {
				b = hi // "N/step" means N..hi by step
			} else {
				b = n
			}
		}
		if a < lo || b > hi || a > b {
			return nil, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// Matches reports whether the expression fires at t (minute precision).
func (c Cron) Matches(t time.Time) bool {
	if !c.min[t.Minute()] || !c.hour[t.Hour()] || !c.mon[int(t.Month())] {
		return false
	}
	domOK := c.dom[t.Day()]
	dowOK := c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowOK
	case c.dowAny:
		return domOK
	default:
		return domOK || dowOK // both restricted: traditional OR
	}
}

// Next returns the first firing time strictly after t, or the zero time if
// none exists within a year (an impossible date like Feb 30).
func (c Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 1)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if c.Matches(t) {
			return t
		}
	}
	return time.Time{}
}
