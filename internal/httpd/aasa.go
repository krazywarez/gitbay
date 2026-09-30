package httpd

import (
	"encoding/json"
	"net/http"
)

// aasaComponent is one applinks path pattern. iOS takes the first
// component that matches; * matches any run of characters, slashes
// included, and ? matches one.
type aasaComponent struct {
	Path    string `json:"/"`
	Exclude bool   `json:"exclude,omitempty"`
}

// aasaComponents lists the pages the iOS app opens. Web-only pages come
// first as exclusions, then the repository sections the app has screens
// for; anything else of three or more segments stays in the browser,
// and what is left is an owner or a repository.
var aasaComponents = func() []aasaComponent {
	var c []aasaComponent
	for _, p := range []string{
		"/.well-known/*", "/static/*", "/api/*", "/admin", "/admin/*",
		"/settings", "/settings/*", "/login", "/logout", "/register",
		"/new", "/explore", "/search", "/healthz", "/privacy", "/favicon.svg",
		"/*/activity.atom", "/*/*/releases.atom", "/*/*/log.atom", "/*/*/log.atom/*",
		"/*/-/queries", "/*/-/queries/*", "/*/-/snippets/new", "/*/-/snippets/*/raw/*",
		"/*/*/settings", "/*/*/fork", "/*/*/search", "/*/*/symbols",
		"/*/*/issues/new", "/*/*/mrs/new", "/*/*/mrs/*/range-diff",
		"/*/*/edit/*", "/*/*/raw/*", "/*/*/archive/*", "/*/*/releases/download/*",
		"/*/*/badge/*", "/*/*/wiki/_raw/*", "/*/*/compare/*", "/*/*/info/*",
	} {
		c = append(c, aasaComponent{Path: p, Exclude: true})
	}
	for _, p := range []string{
		"/bookmarks", "/notifications",
		"/*/-/repositories", "/*/-/bookmarks", "/*/-/people", "/*/-/labels",
		"/*/-/milestones", "/*/-/snippets", "/*/-/snippets/*",
		"/*/*/issues", "/*/*/issues/*", "/*/*/mrs", "/*/*/mrs/*",
		"/*/*/builds", "/*/*/builds/*", "/*/*/tree/*", "/*/*/blob/*",
		"/*/*/blame/*", "/*/*/commit/*", "/*/*/log", "/*/*/log/*",
		"/*/*/refs", "/*/*/compare", "/*/*/releases", "/*/*/milestones",
		"/*/*/labels", "/*/*/wiki", "/*/*/wiki/*",
	} {
		c = append(c, aasaComponent{Path: p})
	}
	return append(c,
		aasaComponent{Path: "/*/*/?*", Exclude: true},
		aasaComponent{Path: "/?*"},
	)
}()

// appleAppSiteAssociation serves the file iOS fetches to decide which
// links open the app ([web] apple_app_ids). Apple requires JSON at this
// exact path with no redirect.
func (s *Server) appleAppSiteAssociation(w http.ResponseWriter, r *http.Request) {
	type detail struct {
		AppIDs     []string        `json:"appIDs"`
		Components []aasaComponent `json:"components"`
	}
	var doc struct {
		Applinks struct {
			Details []detail `json:"details"`
		} `json:"applinks"`
	}
	doc.Applinks.Details = []detail{{AppIDs: s.cfg.Web.AppleAppIDs, Components: aasaComponents}}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}
