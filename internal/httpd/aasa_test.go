package httpd

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// aasaGlob matches one applinks pattern: * is any run of characters,
// slashes included, and ? is one character.
func aasaGlob(pat, s string) bool {
	if pat == "" {
		return s == ""
	}
	switch pat[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if aasaGlob(pat[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && aasaGlob(pat[1:], s[1:])
	}
	return s != "" && s[0] == pat[0] && aasaGlob(pat[1:], s[1:])
}

// opensApp applies a component list the way iOS does: the first match
// decides, and a path nothing matches stays in the browser.
func opensApp(comps []aasaComponent, p string) bool {
	for _, c := range comps {
		if aasaGlob(c.Path, p) {
			return !c.Exclude
		}
	}
	return false
}

func TestAppleAppSiteAssociation(t *testing.T) {
	s := plainServer()
	s.cfg.Web.AppleAppIDs = []string{"ZCNAX3VL9D.org.gitbay.gitbay"}
	w := get(t, s.Handler(), "/.well-known/apple-app-site-association", nil)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	var doc struct {
		Applinks struct {
			Details []struct {
				AppIDs     []string        `json:"appIDs"`
				Components []aasaComponent `json:"components"`
			} `json:"details"`
		} `json:"applinks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, w.Body.String())
	}
	if len(doc.Applinks.Details) != 1 || len(doc.Applinks.Details[0].AppIDs) != 1 ||
		doc.Applinks.Details[0].AppIDs[0] != "ZCNAX3VL9D.org.gitbay.gitbay" {
		t.Fatalf("details: %+v", doc.Applinks.Details)
	}
	comps := doc.Applinks.Details[0].Components
	for _, p := range []string{
		"/krz",
		"/krz/-/repositories",
		"/krz/-/snippets",
		"/krz/-/snippets/abc123",
		"/krz/-/labels",
		"/krz/-/milestones",
		"/krz/gitbay",
		"/krz/gitbay/issues",
		"/krz/gitbay/issues/12",
		"/krz/gitbay/mrs",
		"/krz/gitbay/mrs/7",
		"/krz/gitbay/builds",
		"/krz/gitbay/builds/1510",
		"/krz/gitbay/tree/main/internal/httpd",
		"/krz/gitbay/blob/main/README.org",
		"/krz/gitbay/blame/main/README.org",
		"/krz/gitbay/commit/d0c63c4",
		"/krz/gitbay/log",
		"/krz/gitbay/log/main",
		"/krz/gitbay/refs",
		"/krz/gitbay/compare",
		"/krz/gitbay/releases",
		"/krz/gitbay/milestones",
		"/krz/gitbay/labels",
		"/krz/gitbay/wiki",
		"/krz/gitbay/wiki/Architecture/00-Overview",
		"/krz/gitbay/blob/main/feed.atom",
		"/bookmarks",
		"/notifications",
	} {
		if !opensApp(comps, p) {
			t.Errorf("%s does not open the app", p)
		}
	}
	for _, p := range []string{
		"/",
		"/.well-known/apple-app-site-association",
		"/static/style.css",
		"/static/fonts/x.woff2",
		"/favicon.svg",
		"/api/v1/read",
		"/admin",
		"/admin/users",
		"/settings",
		"/settings/export",
		"/login",
		"/logout",
		"/register",
		"/new",
		"/explore",
		"/search",
		"/healthz",
		"/privacy",
		"/krz/activity.atom",
		"/krz/-/queries",
		"/krz/-/snippets/new",
		"/krz/-/snippets/abc123/raw/a.txt",
		"/krz/gitbay/settings",
		"/krz/gitbay/fork",
		"/krz/gitbay/search",
		"/krz/gitbay/symbols",
		"/krz/gitbay/issues/new",
		"/krz/gitbay/mrs/new",
		"/krz/gitbay/mrs/7/range-diff",
		"/krz/gitbay/edit/main/README.org",
		"/krz/gitbay/raw/main/README.org",
		"/krz/gitbay/archive/main.tar.gz",
		"/krz/gitbay/releases/download/v1.0.0/gitbay.tar.gz",
		"/krz/gitbay/releases.atom",
		"/krz/gitbay/log.atom",
		"/krz/gitbay/badge/build.svg",
		"/krz/gitbay/wiki/_raw/diagram.svg",
		"/krz/gitbay/compare/main...aasa",
		"/krz/gitbay/info/refs",
		"/krz/gitbay/info/lfs/objects/batch",
		"/krz/gitbay/raw/main/docs/tree/a",
		"/krz/gitbay/log.atom/main",
	} {
		if opensApp(comps, p) {
			t.Errorf("%s opens the app", p)
		}
	}
}

// With no app configured the file does not exist.
func TestAppleAppSiteAssociationOff(t *testing.T) {
	for _, r := range plainServer().Routes() {
		if r.Pattern == "/.well-known/apple-app-site-association" {
			t.Fatal("route registered with no apple_app_ids")
		}
	}
}

// appRoutes are the GET patterns the iOS app has a screen for. Every
// other page stays in the browser, so a new route is classified here or
// this test fails.
var appRoutes = map[string]bool{
	"/bookmarks": true, "/notifications": true,
	"/{owner}": true, "/{owner}/-/repositories": true, "/{owner}/-/bookmarks": true,
	"/{owner}/-/people": true, "/{owner}/-/labels": true, "/{owner}/-/milestones": true,
	"/{owner}/-/snippets": true, "/{owner}/-/snippets/{id}": true,
	"/{owner}/{repo}":        true,
	"/{owner}/{repo}/issues": true, "/{owner}/{repo}/issues/{n}": true,
	"/{owner}/{repo}/mrs": true, "/{owner}/{repo}/mrs/{n}": true,
	"/{owner}/{repo}/builds": true, "/{owner}/{repo}/builds/{n}": true,
	"/{owner}/{repo}/tree/{ref}/{path...}": true, "/{owner}/{repo}/blob/{ref}/{path...}": true,
	"/{owner}/{repo}/blame/{ref}/{path...}": true, "/{owner}/{repo}/commit/{sha}": true,
	"/{owner}/{repo}/log": true, "/{owner}/{repo}/log/{ref}": true,
	"/{owner}/{repo}/refs": true, "/{owner}/{repo}/compare": true,
	"/{owner}/{repo}/releases": true, "/{owner}/{repo}/milestones": true,
	"/{owner}/{repo}/labels": true, "/{owner}/{repo}/wiki": true,
	"/{owner}/{repo}/wiki/{page...}": true,
}

func TestAppleAppSiteAssociationCoversEveryRoute(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	cfg.Registration.Mode = "open"
	cfg.API.Enabled = true
	s := New(cfg, nil, nil)
	wild := regexp.MustCompile(`\{[a-z]+(\.\.\.)?\}`)
	for _, r := range s.Routes() {
		if r.Method != "GET" {
			continue
		}
		pat := strings.TrimSuffix(r.Pattern, "{$}")
		p := wild.ReplaceAllStringFunc(pat, func(w string) string {
			if strings.HasSuffix(w, "...}") {
				return "a/b"
			}
			return "x"
		})
		if got := opensApp(aasaComponents, p); got != appRoutes[r.Pattern] {
			t.Errorf("%s (%s): opens app = %v", r.Pattern, p, got)
		}
	}
}
