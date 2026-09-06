package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Every page template parses with the layout at start-up, so a broken
// template fails the process rather than the first visit to its page.
// Package init has already done the work; this asserts it covered every
// file (#116).
func TestEveryPageTemplateParses(t *testing.T) {
	names := Pages()
	if len(names) < 30 {
		t.Fatalf("only %d page templates parsed: %v", len(names), names)
	}
	for _, want := range []string{"tree.html", "blob.html", "mr.html", "issue.html", "dashboard.html", "registered.html"} {
		if _, ok := pages[want]; !ok {
			t.Errorf("%s not parsed", want)
		}
	}
}

// A th with no rule of its own falls back to the browser's centred default
// (#151). The classes that do left-align one are read out of the stylesheet
// rather than listed here, so a new rule does not need a test change.
func TestHeaderRowsAreLeftAligned(t *testing.T) {
	aligned := map[string]bool{}
	for _, rule := range strings.Split(string(StyleCSS), "}") {
		sel, decls, ok := strings.Cut(rule, "{")
		if !ok || !strings.Contains(decls, "text-align: left") || !strings.Contains(sel, "th") {
			continue
		}
		for _, class := range regexp.MustCompile(`\.([a-z-]+)`).FindAllStringSubmatch(sel, -1) {
			aligned[class[1]] = true
		}
	}
	if len(aligned) == 0 {
		t.Fatal("no left-aligning th rule found in the stylesheet")
	}

	classes := regexp.MustCompile(`class="([^"]*)"`)
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		// The table and row tags a header sits under, plus the header row
		// itself: any one of them may carry the class.
		for _, head := range strings.Split(string(b), "<thead>")[1:] {
			open, _, _ := strings.Cut(head, "</tr>")
			before := strings.Split(string(b), "<thead>"+head)[0]
			if i := strings.LastIndex(before, "<table"); i >= 0 {
				open += before[i:]
			}
			hit := false
			for _, m := range classes.FindAllStringSubmatch(open, -1) {
				for _, c := range strings.Fields(m[1]) {
					hit = hit || aligned[c]
				}
			}
			if !hit {
				t.Errorf("%s: header row is not left-aligned by any rule: %.60s", e.Name(), open)
			}
		}
	}
}
