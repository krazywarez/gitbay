package gitutil

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// BlameHunk is a run of consecutive lines attributed to one commit.
type BlameHunk struct {
	SHA         string
	AuthorName  string
	AuthorEmail string
	AuthorUnix  int64
	Summary     string
	StartLine   int // file line number of Lines[0]
	Lines       []string
}

// Blame attributes lines start..end (1-based, inclusive) of path at ref,
// merging consecutive same-commit lines into hunks.
func Blame(dir, ref, path string, start, end int) ([]BlameHunk, error) {
	// blame has no --end-of-options; a resolved sha cannot be an option.
	sha, err := ResolveRef(dir, ref)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "blame", "--porcelain",
		fmt.Sprintf("-L%d,%d", start, end), sha, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git blame %s at %s: %w", path, ref, err)
	}
	type meta struct {
		name, email, summary string
		unix                 int64
	}
	metas := map[string]*meta{}
	var hunks []BlameHunk
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var cur string
	var curLine int
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\t") {
			// Content line; metadata for cur (if any) has already been seen.
			content := line[1:]
			if n := len(hunks) - 1; n >= 0 && hunks[n].SHA == cur &&
				hunks[n].StartLine+len(hunks[n].Lines) == curLine {
				hunks[n].Lines = append(hunks[n].Lines, content)
			} else {
				h := BlameHunk{SHA: cur, StartLine: curLine, Lines: []string{content}}
				if m := metas[cur]; m != nil {
					h.AuthorName, h.AuthorEmail, h.AuthorUnix, h.Summary = m.name, m.email, m.unix, m.summary
				}
				hunks = append(hunks, h)
			}
			continue
		}
		if f := strings.Fields(line); len(f) >= 3 && isSHA(f[0]) {
			cur = f[0]
			curLine, _ = strconv.Atoi(f[2])
			if metas[cur] == nil {
				metas[cur] = &meta{}
			}
			continue
		}
		m := metas[cur]
		if m == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "author "):
			m.name = line[len("author "):]
		case strings.HasPrefix(line, "author-mail "):
			m.email = strings.Trim(line[len("author-mail "):], "<>")
		case strings.HasPrefix(line, "author-time "):
			m.unix, _ = strconv.ParseInt(line[len("author-time "):], 10, 64)
		case strings.HasPrefix(line, "summary "):
			m.summary = line[len("summary "):]
		}
	}
	return hunks, sc.Err()
}

func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
