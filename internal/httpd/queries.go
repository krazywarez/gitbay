package httpd

import (
	"net/http"
	"net/url"
	"strconv"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// queryPerPage is how many rows a saved query's page shows before
// offering the next, through query run's own cursor.
const queryPerPage = 50

// queriesPage lists the viewer's saved queries (/{owner}/-/queries), or
// runs one (/{owner}/-/queries/{name}) through query show and query run,
// the reads the CLI makes. Saved queries are private: under anyone else's
// name the page is not found.
func (s *Server) queriesPage(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if r.PathValue("owner") != viewer.Username {
		s.notFound(w, r)
		return
	}
	name := r.PathValue("name")
	data := struct {
		basePage
		Tab   string
		Saved []control.SavedQueryOut
		Query control.SavedQueryOut
		Count int
		Items []control.QueryItem
		Next  string
	}{basePage: s.baseFor(viewer), Tab: "dashboard"}
	if name == "" {
		if msg, ok := s.runControlInto(viewer, []string{"query", "list"}, &data.Saved); !ok {
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		s.render(w, "queries.html", data)
		return
	}
	switch code, msg := s.runControlIntoCode(viewer, []string{"query", "show", name}, &data.Query); code {
	case protocol.ExitOK:
	case protocol.ExitNotFound:
		s.notFound(w, r)
		return
	default:
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	if data.Query.Count != nil {
		data.Count = *data.Query.Count
	}
	argv := []string{"query", "run", name, "--limit", strconv.Itoa(queryPerPage)}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		argv = append(argv, "--cursor", cursor)
	}
	var page struct {
		Items []control.QueryItem `json:"items"`
		Next  string              `json:"next"`
	}
	switch code, msg := s.runControlIntoCode(viewer, argv, &page); code {
	case protocol.ExitOK:
	case protocol.ExitUsage:
		http.Error(w, msg, http.StatusBadRequest)
		return
	default:
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	data.Items = page.Items
	if page.Next != "" {
		data.Next = "?" + url.Values{"cursor": {page.Next}}.Encode()
	}
	s.render(w, "queries.html", data)
}
