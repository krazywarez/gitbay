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

// ParseTerm reads "<cols>[,<option>]...". Options it does not know are
// ignored, so a newer client's capabilities do not turn an older
// server's output plain. A width that is not a number, or is outside
// 40 to 1000, is plain output.
func ParseTerm(v string) Term {
	parts := strings.Split(v, ",")
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 40 || n > 1000 {
		return Term{}
	}
	t := Term{Cols: n}
	for _, opt := range parts[1:] {
		if opt == "color" {
			t.Color = true
		}
	}
	return t
}

const (
	sgrReset   = "\x1b[0m"
	sgrBold    = "\x1b[1m"
	sgrDim     = "\x1b[2m"
	sgrRed     = "\x1b[31m"
	sgrGreen   = "\x1b[32m"
	sgrYellow  = "\x1b[33m"
	sgrMagenta = "\x1b[35m"
	sgrCyan    = "\x1b[36m"
)

// termSafe replaces the bytes a terminal would act on — ESC, the C0
// controls but tab and newline, DEL, and the C1 controls — with U+FFFD,
// so user text cannot move the cursor, set the clipboard (OSC 52) or
// clear the screen. Carriage return is dropped rather than replaced:
// web forms store CRLF line endings, and a lone CR would let text
// overwrite its own line. Terminal output only: plain output is unchanged.
func termSafe(s string) string {
	unsafe := func(r rune) bool {
		return (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7f && r <= 0x9f)
	}
	if strings.IndexFunc(s, unsafe) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return -1
		case unsafe(r):
			return '\uFFFD'
		}
		return r
	}, s)
}

// safe is termSafe at a terminal and s unchanged in plain output.
func (t Term) safe(s string) string {
	if t.Cols == 0 {
		return s
	}
	return termSafe(s)
}

// paint wraps s in an SGR sequence when colour is on.
func (t Term) paint(sgr, s string) string {
	if !t.Color || sgr == "" || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

// stateColor maps a state word to the web's state tokens: --ok green,
// --done magenta, --bad red, --neutral dim, and yellow for what waits
// on the viewer (the web's orange).
func stateColor(s string) string {
	switch s {
	case "open", "success", "approved", "active", "verified", "ok":
		return sgrGreen
	case "merged":
		return sgrMagenta
	case "failed", "failure", "error", "changes requested", "private",
		"bad_signature", "signed_email_mismatch", "signed_key_expired", "signed_key_revoked":
		return sgrRed
	case "closed", "draft", "pending", "canceled", "cancelled", "archived", "disabled",
		"unsigned", "signed_unknown_key":
		return sgrDim
	case "unverified":
		return sgrYellow
	}
	return ""
}

// paintState colours each word of a state cell: "private, archived"
// is two states, each in its own colour.
func (t Term) paintState(s string) string {
	if !t.Color {
		return s
	}
	words := strings.Split(s, ", ")
	for i, w := range words {
		words[i] = t.paint(stateColor(w), w)
	}
	return strings.Join(words, ", ")
}

// heading is a section label at a terminal: capitalised, no trailing
// colon, bold.
func (t Term) heading(label string) string {
	label = strings.TrimSuffix(label, ":")
	if r, size := utf8.DecodeRuneInString(label); size > 0 {
		label = string(unicode.ToUpper(r)) + label[size:]
	}
	return t.paint(sgrBold, label)
}

// failure is a refusal as a terminal shows it: "error: " in red ahead
// of the message, and a usage line wrapped to the width between its
// bracketed groups, continuation lines indented under the command.
func (t Term) failure(msg string) string {
	lines := strings.Split(termSafe(msg), "\n")
	for i, line := range lines {
		if rest, ok := strings.CutPrefix(line, "usage: "); ok {
			lines[i] = "usage: " + wrapUsage(rest, t.Cols-len("usage: "), strings.Repeat(" ", len("usage: ")))
		} else if i == 0 {
			lines[i] = t.paint(sgrBold+sgrRed, "error:") + " " + line
		}
	}
	return strings.Join(lines, "\n")
}

// wrapUsage packs a usage line into lines of at most width cells,
// breaking only between words outside brackets, so "[--state
// open|closed|all]" and "[--label <l>]" are never split.
func wrapUsage(u string, width int, indent string) string {
	var words []string
	depth, start := 0, 0
	for i, r := range u {
		switch r {
		case '[', '<':
			depth++
		case ']', '>':
			depth = max(0, depth-1)
		case ' ':
			if depth == 0 {
				if i > start {
					words = append(words, u[start:i])
				}
				start = i + 1
			}
		}
	}
	if start < len(u) {
		words = append(words, u[start:])
	}
	var b strings.Builder
	used := 0
	for _, w := range words {
		n := cells(w)
		switch {
		case used == 0:
		case w == "|" || used+1+n > width:
			b.WriteString("\n" + indent)
			used = 0
		default:
			b.WriteByte(' ')
			used++
		}
		b.WriteString(w)
		used += n
	}
	return b.String()
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

// relAge is a stored timestamp as a table shows it at a terminal: "2h
// ago" in the past, "in 2h" in the future, a date beyond eight weeks
// either way.
func relAge(s string, now time.Time) string {
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	var n string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n = fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		n = fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 14*24*time.Hour:
		n = fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 56*24*time.Hour:
		n = fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	default:
		return t.Format("2006-01-02")
	}
	if future {
		return "in " + n
	}
	return n + " ago"
}

// size is a byte count: the number in plain output, KiB and up at a
// terminal.
func (t Term) size(n int64) string {
	if t.Cols == 0 {
		return strconv.FormatInt(n, 10)
	}
	return humanBytes(n)
}

// dur is a number of seconds: "<n>s" in plain output, hours, minutes
// and seconds at a terminal.
func (t Term) dur(secs int64) string {
	if t.Cols == 0 {
		return fmt.Sprintf("%ds", secs)
	}
	return (time.Duration(secs) * time.Second).String()
}
