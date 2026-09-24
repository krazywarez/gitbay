package termtext

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestMarkdownGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []struct {
		name string
		opt  Options
	}{
		{"doc.md.plain.golden", Options{Base: "https://forge.test"}},
		{"doc.md.60.golden", Options{Width: 60, Base: "https://forge.test"}},
		{"doc.md.60color.golden", Options{Width: 60, Color: true, Base: "https://forge.test"}},
	} {
		golden(t, o.name, Markdown(string(src), o.opt))
	}
}

func TestMarkdownWidth(t *testing.T) {
	src, _ := os.ReadFile("testdata/doc.md")
	out := Markdown(string(src), Options{Width: 60, Color: true})
	inCode := false
	for _, line := range strings.Split(out, "\n") {
		plain := stripSGR(line)
		if strings.HasPrefix(plain, "    ") {
			inCode = true
		} else if plain != "" {
			inCode = false
		}
		if !inCode && cells(plain) > 60 {
			t.Errorf("line of %d cells: %q", cells(plain), plain)
		}
	}
}

// TestWrappedColorClosesAtLineEnd covers a bold span that wraps mid-run:
// the style must not bleed across the line break onto the unstyled
// list-hang prefix, and stripping colour must reproduce the plain
// rendering exactly.
func TestWrappedColorClosesAtLineEnd(t *testing.T) {
	src := "- item with **some very long bold phrase spanning many words here** and more text after it to force a wrap"
	color := Markdown(src, Options{Width: 30, Color: true})
	plain := Markdown(src, Options{Width: 30})

	if got := stripSGR(color); got != plain {
		t.Errorf("stripSGR(color) = %q, want %q", got, plain)
	}

	// Both continuation lines land wholly inside the wrapped bold span
	// (the first) or start inside it (the second): the plain prefix
	// always comes before the reopened style, never styled itself, and
	// a run left open at wrap time is closed again at line's end.
	lines := strings.Split(strings.TrimRight(color, "\n"), "\n")
	for _, i := range []int{1, 2} {
		if !strings.HasPrefix(lines[i], "  \x1b[1m") {
			t.Errorf("line %d: styled run starts before the plain prefix, or is missing: %q", i, lines[i])
		}
	}
	if !strings.HasSuffix(lines[1], sgrReset) {
		t.Errorf("line 1: open run never closed: %q", lines[1])
	}
	if want := "  \x1b[1mbold phrase spanning many\x1b[0m"; lines[1] != want {
		t.Errorf("line 1 = %q, want %q", lines[1], want)
	}
	if want := "  \x1b[1mwords here\x1b[0m and more text"; lines[2] != want {
		t.Errorf("line 2 = %q, want %q", lines[2], want)
	}
}

func TestInlineDropsLinkTargets(t *testing.T) {
	got := Inline("referenced in commit [6c4d1e1454](/krz/gitbay/commit/6c4d) by [cmc](/cmc): landing", "md")
	if got != "referenced in commit 6c4d1e1454 by cmc: landing" {
		t.Errorf("Inline = %q", got)
	}
}
