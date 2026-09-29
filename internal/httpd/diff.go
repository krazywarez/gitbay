package httpd

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
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
	Compose bool // render the new-thread form under this line
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
	Rows    []splitRow // the split layout's rows; empty in the unified layout
	Threads int        // threads anchored in this file, so it can stay unfolded
	Open    bool       // rendered unfolded: small files, and anything under review
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
// Content, not Text: the marker is already off it. Taking it off Text here
// meant stripping "+" and then "-", which ate the dash of an added line that
// begins with one.
func sideText(hunk []diffLine, class string) string {
	var b strings.Builder
	for _, l := range hunk {
		if l.Class == "ctx" || l.Class == class {
			b.WriteString(l.Content)
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

// splitRow is one row of the side-by-side layout: a hunk or meta line
// spanning both columns, or a pair of lines. In a run of deletions
// followed by additions the two are zipped, and the shorter side is left
// empty. Old and New point into the file's Lines.
type splitRow struct {
	Kind    string // hunk | meta | pair
	Text    string
	Old     *diffLine
	New     *diffLine
	Threads []diffThread
	OldNote string    // "\ No newline" marker belonging to the old side
	NewNote string    // and to the new side
	Compose *diffLine // the line whose new-thread form opens under this row
}

// diffLayout is the layout a diff page renders in and the links that
// switch it.
type diffLayout struct {
	Split      bool
	UnifiedURL string
	SplitURL   string
	Carry      string // "split" or "unified" when the request chose it, so links keep it
}

// splitFiles fills each file's Rows. It runs after threads and compose
// forms are attached to the lines.
func splitFiles(files []diffFile) {
	for f := range files {
		lines := files[f].Lines
		var rows []splitRow
		for i := 0; i < len(lines); {
			ln := &lines[i]
			switch ln.Class {
			case "hunk", "meta":
				if ln.Class == "meta" && ln.Path == "" && len(rows) > 0 && strings.HasPrefix(ln.Text, `\`) {
					// a marker after a context line: neither side ends in a newline
					if last := &rows[len(rows)-1]; last.Old != nil && last.Old == last.New {
						last.OldNote, last.NewNote = ln.Text, ln.Text
						i++
						continue
					}
				}
				rows = append(rows, splitRow{Kind: ln.Class, Text: ln.Text})
				i++
			case "ctx":
				r := splitRow{Kind: "pair", Old: ln, New: ln, Threads: ln.Threads}
				if ln.Compose {
					r.Compose = ln
				}
				rows = append(rows, r)
				i++
			default:
				var dels, adds []*diffLine
				marker := func() string {
					if i < len(lines) && lines[i].Class == "meta" && strings.HasPrefix(lines[i].Text, `\`) {
						i++
						return lines[i-1].Text
					}
					return ""
				}
				var oldNote, newNote string
				for i < len(lines) && lines[i].Class == "del" {
					dels = append(dels, &lines[i])
					i++
				}
				if len(dels) > 0 {
					oldNote = marker()
				}
				for i < len(lines) && lines[i].Class == "add" {
					adds = append(adds, &lines[i])
					i++
				}
				if len(adds) > 0 {
					newNote = marker()
				}
				if len(dels)+len(adds) == 0 {
					i++ // an unknown class: skip rather than loop
					continue
				}
				first := len(rows)
				for k := 0; k < len(dels) || k < len(adds); k++ {
					r := splitRow{Kind: "pair"}
					for _, l := range []*diffLine{pick(dels, k), pick(adds, k)} {
						if l == nil {
							continue
						}
						if l.Class == "del" {
							r.Old = l
						} else {
							r.New = l
						}
						r.Threads = append(r.Threads, l.Threads...)
						if l.Compose {
							r.Compose = l
						}
					}
					rows = append(rows, r)
				}
				if len(dels) > 0 {
					rows[first+len(dels)-1].OldNote = oldNote
				}
				if len(adds) > 0 {
					rows[first+len(adds)-1].NewNote = newNote
				}
			}
		}
		files[f].Rows = rows
	}
}

func pick(s []*diffLine, i int) *diffLine {
	if i < len(s) {
		return s[i]
	}
	return nil
}

// diffLayoutFor resolves the layout for a request: ?layout= wins, then the
// signed-in account's setting, then unified. The two switch links keep
// every other query parameter.
func (s *Server) diffLayoutFor(r *http.Request) diffLayout {
	q := r.URL.Query()
	l := diffLayout{}
	switch q.Get("layout") {
	case "split":
		l.Split, l.Carry = true, "split"
	case "unified":
		l.Carry = "unified"
	default:
		if s.cfg.Web.Mode == "accounts" {
			if u := s.viewer(r); u.ID != 0 {
				if v, err := s.st.DiffLayout(u.ID); err == nil {
					l.Split = v == "split"
				}
			}
		}
	}
	link := func(v string) string {
		c := url.Values{}
		for k, vs := range q {
			c[k] = vs
		}
		c.Set("layout", v)
		return r.URL.Path + "?" + c.Encode()
	}
	l.UnifiedURL, l.SplitURL = link("unified"), link("split")
	return l
}
