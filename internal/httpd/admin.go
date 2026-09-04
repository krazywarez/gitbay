package httpd

import (
	"net/http"

	"gitbay.org/gitbay/internal/control"
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
	var d control.DashboardOut
	if msg, ok := s.runControlInto(viewer, []string{"dashboard"}, &d); !ok {
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	// Both blocks are admin-only and this handler is admin-gated, so they
	// are present; the payload types them as optional because everyone
	// else's dashboard omits them.
	commit, queues := "", store.Queues{}
	if d.Server != nil {
		commit = d.Server.Commit
	}
	if d.Queues != nil {
		queues = *d.Queues
	}
	s.render(w, "admin.html", struct {
		basePage
		Tab    string
		Commit string
		Queues store.Queues
	}{s.baseFor(viewer), "admin", commit, queues})
}
