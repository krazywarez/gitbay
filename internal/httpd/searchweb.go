package httpd

import (
	"net/http"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// searchResult is one hit as the template renders it: the link is built
// here so the template does not have to know each kind's URL shape.
type searchResult struct {
	control.SearchResult
	Marker string
	Href   string
}

// globalSearch is /search: repositories, issues and merge requests across
// the instance. It is readable without a session — a visitor sees exactly
// the public rows — so it runs control.Search directly rather than
// dispatching a command as a user who may not exist.
func (s *Server) globalSearch(w http.ResponseWriter, r *http.Request) {
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if kind != "repo" && kind != "issue" && kind != "mr" {
		kind = ""
	}
	var kinds []string
	if kind != "" {
		kinds = []string{kind}
	}
	var results []searchResult
	var queryErr string
	if q != "" {
		if len(q) < 2 || len(q) > 200 {
			queryErr = "query must be 2 to 200 characters"
		} else {
			hits, err := control.Search(s.st, s.cfg.Server.Root, viewer.ID, q, kinds)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, h := range hits {
				results = append(results, searchResult{h, control.SearchMarker(h.Kind), searchHref(h)})
			}
		}
	}
	s.render(w, "globalsearch.html", struct {
		basePage
		Query    string
		Kind     string
		QueryErr string
		Results  []searchResult
	}{s.baseFor(viewer), q, kind, queryErr, results})
}

func searchHref(h control.SearchResult) string {
	switch h.Kind {
	case "issue":
		return "/" + h.Repo + "/issues/" + strconv.FormatInt(h.Number, 10)
	case "mr":
		return "/" + h.Repo + "/mrs/" + strconv.FormatInt(h.Number, 10)
	default:
		return "/" + h.Repo
	}
}
