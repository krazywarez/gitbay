package codescan

import "strings"

// joinedStringRule flags implicitly joined strings ("a" "b") anywhere inside
// [...] or {...}. A missing comma only matters where the joined string is
// itself an element of the list or set. Inside a call or a tuple within the
// brackets, as in [r for r in conn.execute("select … " "from …")], the
// strings are one argument split over lines on purpose.
// joinedOutsideLists reads the source to tell those apart.
const joinedStringRule = "python.lang.correctness.common-mistakes.string-concat-in-list"

// keepJoined returns the lines of a Python file a string-concat-in-list hit
// is kept on: every line but those joinedOutsideLists returns.
func keepJoined(src []byte) (map[int]bool, bool) {
	outside, ok := joinedOutsideLists(src)
	if !ok {
		return nil, false
	}
	keep := map[int]bool{}
	for l := 1; l <= strings.Count(string(src), "\n")+1; l++ {
		if !outside[l] {
			keep[l] = true
		}
	}
	return keep, true
}

// joinedOutsideLists returns the lines of a Python file where implicitly
// joined strings start or continue with ( as their innermost bracket, or
// none, and no joined string directly inside [ or { touches the line. ok is
// false when the file does not read to the end with every string and
// bracket closed.
func joinedOutsideLists(src []byte) (lines map[int]bool, ok bool) {
	s := string(src)
	var brackets []byte
	inList, outside := map[int]bool{}, map[int]bool{}
	line := 1
	// The string just read and whether only space, comments and line
	// breaks have followed it: the start of a join.
	prevEnd, prevLine := -1, 0
	joinStart := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
			continue
		case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
			line++
			i += 2
			continue
		case c == '#':
			if end := strings.IndexByte(s[i:], '\n'); end >= 0 {
				i += end
			} else {
				i = len(s)
			}
			continue
		}
		if q, n := stringStart(s, i); n >= 0 {
			end, l, ok := pyString(s, i+n, q, strings.ContainsAny(strings.ToLower(s[i:i+n]), "f"))
			if !ok {
				return nil, false
			}
			if prevEnd >= 0 {
				if joinStart == 0 {
					joinStart = prevLine
				}
				mark := outside
				if len(brackets) > 0 && brackets[len(brackets)-1] != '(' {
					mark = inList
				}
				for k := joinStart; k <= line+l; k++ {
					mark[k] = true
				}
			} else {
				joinStart = 0
			}
			prevEnd, prevLine = end, line
			line += l
			i = end
			continue
		}
		prevEnd, joinStart = -1, 0
		switch c {
		case '(', '[', '{':
			brackets = append(brackets, c)
		case ')', ']', '}':
			if len(brackets) == 0 || brackets[len(brackets)-1] != map[byte]byte{')': '(', ']': '[', '}': '{'}[c] {
				return nil, false
			}
			brackets = brackets[:len(brackets)-1]
		}
		i++
	}
	if len(brackets) > 0 {
		return nil, false
	}
	for l := range inList {
		delete(outside, l)
	}
	return outside, true
}

// stringStart reports whether a string literal opens at i: its quote, and
// the length of its prefix (r, b, u, f and pairs of them), or -1.
func stringStart(s string, i int) (quote string, n int) {
	j := i
	for j < len(s) && j-i < 2 && strings.IndexByte("rRbBuUfF", s[j]) >= 0 {
		j++
	}
	if j < len(s) && (s[j] == '"' || s[j] == '\'') {
		if j > i && isIdentByte(s, i-1) {
			return "", -1
		}
		q := s[j : j+1]
		if strings.HasPrefix(s[j:], strings.Repeat(q, 3)) {
			q = strings.Repeat(q, 3)
		}
		return q, j - i
	}
	return "", -1
}

func isIdentByte(s string, i int) bool {
	if i < 0 {
		return false
	}
	c := s[i]
	return c == '_' || isLetter(c) || c >= '0' && c <= '9' || c >= 0x80
}

// pyString reads a string from i, the start of its quote q, to just past its
// closing quote. lines is how many line breaks it spans. In an f-string the
// expressions in braces are skipped with their own strings and brackets.
func pyString(s string, i int, q string, f bool) (end, lines int, ok bool) {
	j := i + len(q)
	for j < len(s) {
		c := s[j]
		switch {
		case strings.HasPrefix(s[j:], q):
			return j + len(q), lines, true
		case c == '\\':
			if j+1 < len(s) && s[j+1] == '\n' {
				lines++
			}
			j += 2
			continue
		case c == '\n':
			if len(q) == 1 {
				return 0, 0, false
			}
			lines++
		case f && c == '{' && strings.HasPrefix(s[j:], "{{"):
			j += 2
			continue
		case f && c == '{':
			n, l, ok := fieldEnd(s, j+1)
			if !ok {
				return 0, 0, false
			}
			lines += l
			j = n
			continue
		}
		j++
	}
	return 0, 0, false
}

// fieldEnd reads an f-string replacement field from i, just past its {, to
// just past its }.
func fieldEnd(s string, i int) (end, lines int, ok bool) {
	depth := 0
	for i < len(s) {
		c := s[i]
		if q, n := stringStart(s, i); n >= 0 {
			e, l, ok := pyString(s, i+n, q, strings.ContainsAny(strings.ToLower(s[i:i+n]), "f"))
			if !ok {
				return 0, 0, false
			}
			lines += l
			i = e
			continue
		}
		switch c {
		case '\n':
			lines++
		case '(', '[', '{':
			depth++
		case ')', ']':
			depth--
		case '}':
			if depth == 0 {
				return i + 1, lines, true
			}
			depth--
		}
		i++
	}
	return 0, 0, false
}
