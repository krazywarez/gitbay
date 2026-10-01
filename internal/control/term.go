package control

import (
	"fmt"
	"regexp"
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
	// TrueColor is 24-bit colour, for a label's own colour.
	TrueColor bool
	// Links is OSC 8 hyperlinks, for a reference's page.
	Links bool
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
		switch opt {
		case "color":
			t.Color = true
		case "truecolor":
			t.TrueColor = true
		case "links":
			t.Links = true
		}
	}
	t.TrueColor = t.TrueColor && t.Color
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
	case "closed", "draft", "pending", "canceled", "cancelled", "archived", "disabled", "skipped",
		"unsigned", "signed_unknown_key":
		return sgrDim
	case "unverified":
		return sgrYellow
	}
	return ""
}

// link makes s a hyperlink to url when the terminal shows them (OSC 8).
// A url with a control byte in it is left out rather than sent.
func (t Term) link(url, s string) string {
	if !t.Links || url == "" || s == "" || strings.IndexFunc(url, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return s
	}
	return "\x1b]8;;" + url + "\x1b\\" + s + "\x1b]8;;\x1b\\"
}

// rgb is the SGR sequence for a "#rrggbb" colour as a 24-bit
// foreground, or "" when hex is not one.
func rgb(hex string) string {
	var r, g, b int
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	if n, err := fmt.Sscanf(hex[1:], "%02x%02x%02x", &r, &g, &b); err != nil || n != 3 {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// swatch paints the dot of a "● #rrggbb" label colour cell in that
// colour; anything else is returned as it is.
func (t Term) swatch(s string) string {
	hex, ok := strings.CutPrefix(s, "● ")
	if !ok || !t.TrueColor || rgb(hex) == "" {
		return s
	}
	return t.paint(rgb(hex), "●") + " " + hex
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

// diff is a unified diff, a git stat block, or range-diff output as a
// terminal shows it: made safe, and with colour file headers bold, hunk
// headers cyan, added lines green and removed lines red. Plain output
// and a terminal without colour get it unpainted.
func (t Term) diff(patch string) string {
	if t.Cols == 0 {
		return patch
	}
	patch = termSafe(patch)
	if !t.Color {
		return patch
	}
	lines := strings.Split(patch, "\n")
	inDiff := false
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inDiff = true
			lines[i] = t.paint(sgrBold, l)
		case !inDiff:
			lines[i] = t.paintPreamble(l)
		case strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "),
			strings.HasPrefix(l, "index "), strings.HasPrefix(l, "new file mode"),
			strings.HasPrefix(l, "deleted file mode"), strings.HasPrefix(l, "old mode"),
			strings.HasPrefix(l, "new mode"), strings.HasPrefix(l, "similarity index"),
			strings.HasPrefix(l, "rename from"), strings.HasPrefix(l, "rename to"),
			strings.HasPrefix(l, "Binary files"):
			lines[i] = t.paint(sgrBold, l)
		default:
			lines[i] = t.paintDiffLine(l)
		}
	}
	return strings.Join(lines, "\n")
}

// paintPreamble colours what comes before the first file in a diff:
// a stat line's +/- bar, or a range-diff line (a commit pair, bold, or
// an indented line of the diff between the two patches).
func (t Term) paintPreamble(l string) string {
	if rest, ok := strings.CutPrefix(l, "    "); ok {
		return "    " + t.paintDiffLine(rest)
	}
	if path, bar, ok := strings.Cut(l, " | "); ok && strings.HasPrefix(l, " ") {
		n := strings.TrimRight(bar, "+-")
		plus := strings.Count(bar[len(n):], "+")
		return path + " | " + n + t.paint(sgrGreen, strings.Repeat("+", plus)) +
			t.paint(sgrRed, bar[len(n)+plus:])
	}
	if rangePair(l) {
		return t.paint(sgrBold, l)
	}
	return l
}

// paintDiffLine colours one line of a hunk by its first byte.
func (t Term) paintDiffLine(l string) string {
	switch {
	case strings.HasPrefix(l, "@@"):
		if end := strings.Index(l[2:], "@@"); end >= 0 {
			return t.paint(sgrCyan, l[:end+4]) + l[end+4:]
		}
		return t.paint(sgrCyan, l)
	case strings.HasPrefix(l, "+"):
		return t.paint(sgrGreen, l)
	case strings.HasPrefix(l, "-"):
		return t.paint(sgrRed, l)
	}
	return l
}

// rangePair reports whether l is a range-diff commit pair line:
// "1:  abc1234 = 1:  def5678 subject", either side possibly "-:  -------".
func rangePair(l string) bool {
	f := strings.Fields(l)
	return len(f) >= 5 && strings.HasSuffix(f[0], ":") && strings.HasSuffix(f[3], ":") &&
		strings.ContainsAny(f[2], "=!<>") && len(f[2]) == 1
}

// toolSGR matches the colour sequences build tools print.
var toolSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// buildLog is a build log as a terminal shows it: the tools' own colour
// dropped and the rest made safe, since a repository's build writes
// it; step lines ("$ make test") bold, the failed step's in red.
func (t Term) buildLog(log, failed string) string {
	log = termSafe(toolSGR.ReplaceAllString(log, ""))
	if !t.Color {
		return log
	}
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		step, ok := strings.CutPrefix(l, "$ ")
		switch {
		case !ok:
		case failed != "" && step == failed:
			lines[i] = t.paint(sgrBold+sgrRed, l)
		default:
			lines[i] = t.paint(sgrBold, l)
		}
	}
	return strings.Join(lines, "\n")
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
