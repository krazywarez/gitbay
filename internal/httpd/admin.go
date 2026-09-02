package httpd

import (
	"net/http"

	"gitbay.org/gitbay/internal/store"
)

// adminPage is the operator's view: the running build and every worker
// queue, read through the dashboard command's admin-only block. A
// non-admin gets the same 404 a missing page would, so the URL confirms
// nothing.
func (s *Server) adminPage(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if !viewer.IsAdmin {
		s.notFound(w, r)
		return
	}
	var d struct {
		Server struct {
			Commit string `json:"commit"`
		} `json:"server"`
		Queues store.Queues `json:"queues"`
	}
	if msg, ok := s.runControlInto(viewer, []string{"dashboard"}, &d); !ok {
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	s.render(w, "admin.html", struct {
		basePage
		Tab    string
		Commit string
		Queues store.Queues
	}{s.baseFor(viewer), "admin", d.Server.Commit, d.Queues})
}
