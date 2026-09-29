// Package suggest reads the replacement lines a review comment proposes
// in a fenced suggestion block, and applies them to a file's anchored
// line range. The server's apply and the CLI's local apply share it, so
// both produce the same bytes.
package suggest

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Parse returns the lines of the one ```suggestion block in body. found
// is false when there is none. An empty block proposes deleting the
// range; a block holding one empty line proposes a blank line.
func Parse(body string) (lines []string, found bool, err error) {
	start, end, err := locate(body)
	if err != nil || start < 0 {
		return nil, false, err
	}
	all := strings.Split(normalize(body), "\n")
	return append([]string{}, all[start+1:end]...), true, nil
}

// Strip returns body without its suggestion block, for rendering the
// prose around a suggestion that is shown as a diff instead.
func Strip(body string) string {
	start, end, err := locate(body)
	if err != nil || start < 0 {
		return body
	}
	all := strings.Split(normalize(body), "\n")
	return strings.TrimSpace(strings.Join(append(all[:start:start], all[end+1:]...), "\n"))
}

func normalize(body string) string { return strings.ReplaceAll(body, "\r\n", "\n") }

// locate finds the suggestion block's opening and closing fence lines.
// start is -1 when there is no block.
func locate(body string) (start, end int, err error) {
	all := strings.Split(normalize(body), "\n")
	start = -1
	for i := 0; i < len(all); i++ {
		fence, ok := opening(all[i])
		if !ok {
			continue
		}
		if start >= 0 {
			return -1, -1, errors.New("a comment carries one suggestion block")
		}
		j := i + 1
		for ; j < len(all); j++ {
			if closing(all[j], fence) {
				break
			}
		}
		if j == len(all) {
			return -1, -1, errors.New("the suggestion block is not closed")
		}
		start, end = i, j
		i = j
	}
	return start, end, nil
}

// opening reports whether line opens a suggestion block, and the length
// of its backtick fence.
func opening(line string) (int, bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return 0, false
	}
	n := len(t) - len(strings.TrimLeft(t, "`"))
	if n < 3 {
		return 0, false
	}
	return n, strings.TrimSpace(t[n:]) == "suggestion"
}

func closing(line string, fence int) bool {
	t := strings.TrimSpace(line)
	return len(t) >= fence && strings.Trim(t, "`") == ""
}

// Text is the replacement as one string, every line ending in a newline:
// "" deletes the range and "\n" is one blank line.
func Text(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// FromText undoes Text.
func FromText(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// split cuts content into lines, each keeping its terminator. The last
// line has none when the file does not end in a newline.
func split(content []byte) [][]byte {
	var out [][]byte
	for len(content) > 0 {
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			out = append(out, content)
			break
		}
		out = append(out, content[:i+1])
		content = content[i+1:]
	}
	return out
}

// Range returns lines start through end (1-based, inclusive) of content
// with their terminators, and false when the file is shorter than that.
func Range(content []byte, start, end int) ([]byte, bool) {
	lines := split(content)
	if start < 1 || end < start || end > len(lines) {
		return nil, false
	}
	return bytes.Join(lines[start-1:end], nil), true
}

// Apply replaces lines start through end of content with repl. The
// replacement takes the line ending the file uses there, CRLF or LF, and
// the last replacement line keeps whatever ended the range, so a range
// at the end of a file with no final newline still has none.
func Apply(content []byte, start, end int, repl []string) ([]byte, error) {
	lines := split(content)
	if start < 1 || end < start {
		return nil, fmt.Errorf("bad line range %d-%d", start, end)
	}
	if end > len(lines) {
		return nil, fmt.Errorf("the file has %d lines; the suggestion ends at line %d", len(lines), end)
	}
	last := lines[end-1]
	eol, lastEOL := []byte("\n"), []byte{}
	switch {
	case bytes.HasSuffix(last, []byte("\r\n")):
		eol, lastEOL = []byte("\r\n"), []byte("\r\n")
	case bytes.HasSuffix(last, []byte("\n")):
		lastEOL = []byte("\n")
	case end > 1 && bytes.HasSuffix(lines[end-2], []byte("\r\n")):
		eol = []byte("\r\n")
	}
	var b bytes.Buffer
	for _, l := range lines[:start-1] {
		b.Write(l)
	}
	for i, r := range repl {
		b.WriteString(r)
		if i == len(repl)-1 {
			b.Write(lastEOL)
		} else {
			b.Write(eol)
		}
	}
	for _, l := range lines[end:] {
		b.Write(l)
	}
	return b.Bytes(), nil
}
