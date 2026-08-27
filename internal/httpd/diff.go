package httpd

import (
	"bytes"
	"html/template"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

type diffLine struct {
	Class   string        // meta | hunk | add | del | ctx
	Text    string        // the raw diff line, marker included
	Content string        // the line without its +/- marker
	Code    template.HTML // Content highlighted; empty when the type is unknown
	Path    string        // file this line belongs to
	NewLine int64         // line number in the new file (0 when absent)
	OldLine int64         // line number in the old file (0 when absent)
	Threads []diffThread
}

// diffFile is one file's worth of a unified diff: the header lines are
// consumed into the fields here, so the template renders a section rather
// than replaying "diff --git" at the reader.
type diffFile struct {
	Path    string // new path; the old one for a delete
	OldPath string // set only on a rename
	Status  string // added | deleted | renamed | modified
	Adds    int
	Dels    int
	Binary  bool
	Lines   []diffLine
	Threads int  // threads anchored in this file, so it can stay unfolded
	Open    bool // rendered unfolded: small files, and anything under review
}

type diffStat struct{ Files, Adds, Dels int }

var hunkPat = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseDiff splits a unified diff into per-file sections, tracking old and
// new line numbers so review threads can anchor inline.
func parseDiff(patch string) []diffFile {
	var files []diffFile
	var cur *diffFile
	var oldN, newN int64

	// Paths arrive both in "diff --git a/x b/y" and in the ---/+++ pair.
	// The latter is authoritative (it survives quoting oddities), so the
	// git line only opens the section.
	start := func() *diffFile {
		files = append(files, diffFile{Status: "modified"})
		return &files[len(files)-1]
	}

	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = start()
			if a, b, ok := gitHeaderPaths(l); ok {
				cur.OldPath, cur.Path = a, b
			}
			continue
		case cur == nil:
			continue // preamble before the first file
		case strings.HasPrefix(l, "new file mode"):
			cur.Status = "added"
			continue
		case strings.HasPrefix(l, "deleted file mode"):
			cur.Status = "deleted"
			continue
		case strings.HasPrefix(l, "rename from "):
			cur.Status, cur.OldPath = "renamed", strings.TrimPrefix(l, "rename from ")
			continue
		case strings.HasPrefix(l, "rename to "):
			cur.Status, cur.Path = "renamed", strings.TrimPrefix(l, "rename to ")
			continue
		case strings.HasPrefix(l, "Binary files "), strings.HasPrefix(l, "GIT binary patch"):
			cur.Binary = true
			continue
		case strings.HasPrefix(l, "--- "):
			if p := strings.TrimPrefix(l, "--- "); p != "/dev/null" {
				cur.OldPath = strings.TrimPrefix(p, "a/")
			}
			continue
		case strings.HasPrefix(l, "+++ "):
			if p := strings.TrimPrefix(l, "+++ "); p != "/dev/null" {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
			continue
		case strings.HasPrefix(l, "index "), strings.HasPrefix(l, "old mode "),
			strings.HasPrefix(l, "new mode "), strings.HasPrefix(l, "similarity index "),
			strings.HasPrefix(l, "dissimilarity index "):
			continue
		}

		d := diffLine{Text: l, Content: l, Path: cur.Path}
		switch {
		case strings.HasPrefix(l, "@@"):
			d.Class, d.Path = "hunk", ""
			if m := hunkPat.FindStringSubmatch(l); m != nil {
				oldN, _ = strconv.ParseInt(m[1], 10, 64)
				newN, _ = strconv.ParseInt(m[2], 10, 64)
			}
		case strings.HasPrefix(l, "+"):
			d.Class, d.Content, d.NewLine = "add", l[1:], newN
			newN++
			cur.Adds++
		case strings.HasPrefix(l, "-"):
			d.Class, d.Content, d.OldLine = "del", l[1:], oldN
			oldN++
			cur.Dels++
		case l == `\ No newline at end of file`:
			d.Class, d.Path = "meta", ""
		case l == "":
			continue // trailing newline from the split
		default:
			d.Class, d.Content, d.OldLine, d.NewLine = "ctx", l[1:], oldN, newN
			oldN++
			newN++
		}
		cur.Lines = append(cur.Lines, d)
	}

	for i := range files {
		if files[i].Path == "" {
			files[i].Path = files[i].OldPath
		}
		if files[i].Status == "renamed" && files[i].OldPath == files[i].Path {
			files[i].Status = "modified"
		}
		highlightFile(&files[i])
		// Big files fold shut so a large diff is navigable; anything
		// carrying review threads stays open regardless.
		files[i].Open = len(files[i].Lines) <= 300
	}
	return files
}

// gitHeaderPaths pulls both paths out of a "diff --git a/x b/y" line. Paths
// with spaces make this ambiguous in general; git quotes those, and the
// ---/+++ lines correct us either way.
func gitHeaderPaths(l string) (string, string, bool) {
	rest := strings.TrimPrefix(l, "diff --git ")
	i := strings.Index(rest, " b/")
	if !strings.HasPrefix(rest, "a/") || i < 0 {
		return "", "", false
	}
	return rest[2:i], rest[i+3:], true
}

// diffFormatter is the blob formatter without line numbers: the diff
// supplies its own gutters. PreventSurroundingPre also drops chroma's
// per-line <span class="line"> wrapper, which the generated CSS gives
// display:flex — inside a diff row that breaks the +/- marker onto a line
// of its own.
var diffFormatter = html.New(html.WithClasses(true), html.PreventSurroundingPre(true))

// highlightFile syntax-highlights a file's diff content one hunk at a time,
// each side separately. A hunk's context+deletions are contiguous lines of
// the old file and its context+additions are contiguous lines of the new
// one, so each side lexes as real code — highlighting line by line instead
// would break every multi-line string and block comment.
func highlightFile(f *diffFile) {
	if f.Binary || len(f.Lines) == 0 {
		return
	}
	lexer := lexers.Match(f.Path)
	if lexer == nil {
		return // unknown type: plain text reads fine, and guessing is worse
	}
	for start := 0; start < len(f.Lines); {
		if f.Lines[start].Class == "hunk" || f.Lines[start].Class == "meta" {
			start++
			continue
		}
		end := start
		for end < len(f.Lines) && f.Lines[end].Class != "hunk" && f.Lines[end].Class != "meta" {
			end++
		}
		hunk := f.Lines[start:end]
		assign(hunk, "del", highlightLines(lexer, sideText(hunk, "del")))
		assign(hunk, "add", highlightLines(lexer, sideText(hunk, "add")))
		start = end
	}
}

// sideText joins one side of a hunk: context plus the given change class.
func sideText(hunk []diffLine, class string) string {
	var b strings.Builder
	for _, l := range hunk {
		if l.Class == "ctx" || l.Class == class {
			b.WriteString(strings.TrimPrefix(strings.TrimPrefix(l.Text, "+"), "-"))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// assign hands highlighted lines back to the diff lines they came from.
// Context lines take whichever side ran last; both sides hold identical
// text there, so the result is the same either way.
func assign(hunk []diffLine, class string, out []template.HTML) {
	i := 0
	for j := range hunk {
		if hunk[j].Class != "ctx" && hunk[j].Class != class {
			continue
		}
		if i < len(out) {
			hunk[j].Code = out[i]
		}
		i++
	}
}

// highlightLines formats source and splits the result back into lines.
// chroma emits tokens that may span newlines, so the split happens on the
// rendered HTML with tags reopened per line.
func highlightLines(lexer chroma.Lexer, src string) []template.HTML {
	if src == "" {
		return nil
	}
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	if err := diffFormatter.Format(&buf, styles.Get(lightStyle), it); err != nil {
		return nil
	}
	body := strings.TrimSuffix(buf.String(), "\n")

	var out []template.HTML
	for _, line := range splitHighlighted(body) {
		out = append(out, template.HTML(line))
	}
	return out
}

// splitHighlighted breaks formatted HTML on newlines that sit outside a
// tag, closing and reopening the spans that straddle the break so every
// line is balanced markup on its own.
func splitHighlighted(body string) []string {
	var lines []string
	var open []string
	var cur strings.Builder
	for i := 0; i < len(body); {
		switch body[i] {
		case '<':
			j := strings.IndexByte(body[i:], '>')
			if j < 0 {
				cur.WriteString(body[i:])
				i = len(body)
				continue
			}
			tag := body[i : i+j+1]
			if strings.HasPrefix(tag, "</") {
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			} else if !strings.HasSuffix(tag, "/>") {
				open = append(open, tag)
			}
			cur.WriteString(tag)
			i += j + 1
		case '\n':
			for range open {
				cur.WriteString("</span>")
			}
			lines = append(lines, cur.String())
			cur.Reset()
			for _, t := range open {
				cur.WriteString(t)
			}
			i++
		default:
			cur.WriteByte(body[i])
			i++
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// statOf totals a parsed diff for the summary line.
func statOf(files []diffFile) diffStat {
	st := diffStat{Files: len(files)}
	for _, f := range files {
		st.Adds += f.Adds
		st.Dels += f.Dels
	}
	return st
}
