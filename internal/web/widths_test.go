package web

import (
	"regexp"
	"strings"
	"testing"
)

// List pages and the dashboard render at the container width; text pages
// keep the reading cap (desktop layout spec).
func TestListPagesAreWide(t *testing.T) {
	wide := []string{"dashboard.html", "issues.html", "mrs.html", "explore.html", "notifications.html", "globalsearch.html", "builds.html"}
	for _, name := range wide {
		src, err := templateFS.ReadFile("templates/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(src), `{{define "width"}}wide{{end}}`) {
			t.Errorf("%s does not declare width wide", name)
		}
	}
	for _, name := range []string{"issue.html", "wiki.html", "owner.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		if strings.Contains(string(src), `{{define "width"}}wide{{end}}`) {
			t.Errorf("%s is a text page and must not be wide", name)
		}
	}
	for _, name := range []string{"issues.html", "mrs.html", "notifications.html", "globalsearch.html", "dashboard.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		if !strings.Contains(string(src), `<ul class="issuelist rows">`) {
			t.Errorf("%s does not use one-line rows", name)
		}
	}
	if src, _ := templateFS.ReadFile("templates/explore.html"); !strings.Contains(string(src), `<ul class="repolist rows">`) {
		t.Error("explore.html does not use one-line rows")
	}
	// Settings pages carry a section column: every section id has a link
	// in the column, and the page is wide with the narrow grid.
	for _, name := range []string{"settings.html", "account.html", "admin.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		s := string(src)
		if !strings.HasPrefix(s, `{{define "width"}}wide{{end}}`) || !strings.Contains(s, `<div class="withcol narrow">`) {
			t.Errorf("%s lacks the narrow column layout", name)
		}
		for _, m := range regexp.MustCompile(`<section id="([a-z]+)"`).FindAllStringSubmatch(s, -1) {
			if !strings.Contains(s, `href="#`+m[1]+`"`) {
				t.Errorf("%s: section %q has no link in the column", name, m[1])
			}
		}
	}
}
