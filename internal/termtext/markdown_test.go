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

func TestInlineDropsLinkTargets(t *testing.T) {
	got := Inline("referenced in commit [6c4d1e1454](/krz/gitbay/commit/6c4d) by [cmc](/cmc): landing", "md")
	if got != "referenced in commit 6c4d1e1454 by cmc: landing" {
		t.Errorf("Inline = %q", got)
	}
}
