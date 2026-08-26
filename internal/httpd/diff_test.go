package httpd

import (
	"strings"
	"testing"
)

const samplePatch = `diff --git a/main.go b/main.go
index 1234567..89abcde 100644
--- a/main.go
+++ b/main.go
@@ -1,6 +1,7 @@
 package main
 
-func old() string {
-	return "a"
+func replaced() string {
+	// a comment
+	return "b"
 }
diff --git a/notes.txt b/README.md
similarity index 60%
rename from notes.txt
rename to README.md
--- a/notes.txt
+++ b/README.md
@@ -1 +1 @@
-old title
+new title
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..1111111
Binary files /dev/null and b/logo.png differ
`

func TestParseDiffFiles(t *testing.T) {
	files := parseDiff(samplePatch)
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}

	if got := files[0].Path; got != "main.go" {
		t.Errorf("file 0 path = %q", got)
	}
	if files[0].Adds != 3 || files[0].Dels != 2 {
		t.Errorf("main.go stat = +%d -%d, want +3 -2", files[0].Adds, files[0].Dels)
	}
	if files[0].Status != "modified" {
		t.Errorf("main.go status = %q", files[0].Status)
	}

	if files[1].Status != "renamed" || files[1].OldPath != "notes.txt" || files[1].Path != "README.md" {
		t.Errorf("rename = %q %q -> %q", files[1].Status, files[1].OldPath, files[1].Path)
	}

	if !files[2].Binary || files[2].Status != "added" || files[2].Path != "logo.png" {
		t.Errorf("binary add = %+v", files[2])
	}
	if len(files[2].Lines) != 0 {
		t.Errorf("binary file has %d lines, want none", len(files[2].Lines))
	}

	if st := statOf(files); st.Files != 3 || st.Adds != 4 || st.Dels != 3 {
		t.Errorf("stat = %+v, want 3 files +4 -3", st)
	}
}

// Line numbers anchor review threads, so an off-by-one here silently moves
// every comment on a merge request.
func TestParseDiffLineNumbers(t *testing.T) {
	lines := parseDiff(samplePatch)[0].Lines
	type want struct {
		class    string
		old, new int64
		content  string
	}
	wants := []want{
		{"hunk", 0, 0, ""},
		{"ctx", 1, 1, "package main"},
		{"ctx", 2, 2, ""},
		{"del", 3, 0, "func old() string {"},
		{"del", 4, 0, "\treturn \"a\""},
		{"add", 0, 3, "func replaced() string {"},
		{"add", 0, 4, "\t// a comment"},
		{"add", 0, 5, "\treturn \"b\""},
		{"ctx", 5, 6, "}"},
	}
	if len(lines) != len(wants) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(wants), lines)
	}
	for i, w := range wants {
		got := lines[i]
		if got.Class != w.class || got.OldLine != w.old || got.NewLine != w.new {
			t.Errorf("line %d = %s old=%d new=%d, want %s old=%d new=%d",
				i, got.Class, got.OldLine, got.NewLine, w.class, w.old, w.new)
		}
		if w.class != "hunk" && got.Content != w.content {
			t.Errorf("line %d content = %q, want %q", i, got.Content, w.content)
		}
	}
}

// Highlighting runs per hunk side and is mapped back line by line; the
// mapping is what breaks, so check that every code line got markup and that
// it still says what the source said.
func TestParseDiffHighlighting(t *testing.T) {
	for _, l := range parseDiff(samplePatch)[0].Lines {
		if l.Class == "hunk" || l.Content == "" {
			continue
		}
		if l.Code == "" {
			t.Errorf("%s line %q got no highlighted markup", l.Class, l.Content)
			continue
		}
		if text := strings.TrimSpace(stripTags(string(l.Code))); text != strings.TrimSpace(l.Content) {
			t.Errorf("highlighted %q reads as %q", l.Content, text)
		}
	}
}

// A file whose type chroma does not know renders as plain text rather than
// being guessed at.
func TestParseDiffUnknownType(t *testing.T) {
	files := parseDiff(`diff --git a/x.zzz b/x.zzz
--- a/x.zzz
+++ b/x.zzz
@@ -1 +1 @@
-before
+after
`)
	if len(files) != 1 {
		t.Fatalf("got %d files", len(files))
	}
	for _, l := range files[0].Lines {
		if l.Class == "hunk" {
			continue
		}
		if l.Code != "" {
			t.Errorf("unknown type got markup: %q", l.Code)
		}
	}
}

// splitHighlighted has to close and reopen spans that straddle a newline,
// or one unterminated tag swallows the rest of the file.
func TestSplitHighlightedBalancesTags(t *testing.T) {
	got := splitHighlighted(`<span class="c">line one
line two</span>plain`)
	want := []string{`<span class="c">line one</span>`, `<span class="c">line two</span>plain`}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), "&#34;", `"`)
}
