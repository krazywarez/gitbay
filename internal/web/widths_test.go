package web

import (
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
}
