package control

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// Term is what the client said about its terminal (GITBAY_TERM). The
// zero value is plain output: tab-separated rows, no header, no colour,
// which is what stock ssh, the API and the web get.
type Term struct {
	Cols  int
	Color bool
}

// ParseTerm reads "<cols>[,color]". Anything else, or a width outside
// 40 to 1000, is plain output.
func ParseTerm(v string) Term {
	cols, opt, hasOpt := strings.Cut(v, ",")
	n, err := strconv.Atoi(cols)
	if err != nil || n < 40 || n > 1000 {
		return Term{}
	}
	switch {
	case !hasOpt:
		return Term{Cols: n}
	case opt == "color":
		return Term{Cols: n, Color: true}
	}
	return Term{}
}

const (
	sgrReset     = "\x1b[0m"
	sgrBold      = "\x1b[1m"
	sgrDim       = "\x1b[2m"
	sgrUnderline = "\x1b[4m"
	sgrRed       = "\x1b[31m"
	sgrGreen     = "\x1b[32m"
	sgrMagenta   = "\x1b[35m"
)

// paint wraps s in an SGR sequence when colour is on.
func (t Term) paint(sgr, s string) string {
	if !t.Color || sgr == "" || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

// stateColor maps a state word to the web's state tokens: --ok green,
// --done magenta, --bad red, --neutral dim.
func stateColor(s string) string {
	switch s {
	case "open", "success", "approved", "active":
		return sgrGreen
	case "merged":
		return sgrMagenta
	case "failed", "failure", "error", "changes requested":
		return sgrRed
	case "closed", "draft", "pending", "canceled", "cancelled", "archived", "disabled":
		return sgrDim
	}
	return ""
}

// cells is the width of s in terminal cells: SGR sequences and
// combining marks take none, East Asian wide and fullwidth runes two.
func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n += runeCells(r)
	}
	return n
}

func runeCells(r rune) int {
	if unicode.In(r, unicode.Mn, unicode.Me) || r == '‍' {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// clip cuts s to at most w cells, ending in "…" when anything was cut.
// s must carry no SGR sequences: colour goes on after clipping.
func clip(s string, w int) string {
	if cells(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rc := runeCells(r)
		if used+rc > w-1 {
			break
		}
		b.WriteRune(r)
		used += rc
	}
	return b.String() + "…"
}

// pad right-pads s with spaces to w cells.
func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-cells(s)))
}

// termNow is the clock ages are measured against; tests pin it.
var termNow = time.Now

// parseStamp reads a stored timestamp: RFC3339 as the store writes it,
// or SQLite's datetime() form.
func parseStamp(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// stamp is a stored timestamp in plain output: RFC3339 to the second.
func stamp(s string) string {
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	return t.Format("2006-01-02T15:04:05Z")
}

// relAge is a stored timestamp as a table shows it at a terminal.
func relAge(s string, now time.Time) string {
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	d := max(now.Sub(t), 0)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case d < 56*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d/(7*24*time.Hour)))
	}
	return t.Format("2006-01-02")
}
