package httpd

import (
	"net/http"

	"gitbay.org/gitbay/internal/store"
)

// rail is the viewer's cross-repo state the layout needs. The rail itself
// is icons only, so what is left is the unread count on its bell; the
// pinned repositories it used to list are rendered by the dashboard.
type rail struct {
	Unread int
}

// basePage is what the layout needs on every page, repo or not. Page
// structs embed it so the rail and the site name are always in scope.
type basePage struct {
	// Site is the instance's display name; Host is the name a command or
	// URL must use. Anything copy-pasteable takes Host.
	Site   string
	Host   string
	Viewer string
	Admin  bool // the viewer is an instance admin: the rail shows /admin
	Rail   rail
	// Theme is stamped on <html> as data-theme: light or dark when the
	// viewer chose one, empty when the browser's own scheme decides.
	Theme string
}

// base builds the layout-wide data for a request that has not already
// resolved a viewer.
func (s *Server) base(r *http.Request) basePage {
	if s.cfg.Web.Mode != "accounts" {
		return basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}
	}
	return s.baseFor(s.viewer(r))
}

// baseFor is base for a handler that already holds the viewer, so the
// session lookup is not repeated.
func (s *Server) baseFor(viewer store.User) basePage {
	b := basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}
	if viewer.ID == 0 {
		return b
	}
	b.Viewer = viewer.Username
	b.Admin = viewer.IsAdmin
	b.Rail = s.railFor(viewer)
	if theme, err := s.st.Theme(viewer.ID); err == nil && theme != "system" {
		b.Theme = theme
	}
	return b
}

// railFor collects the viewer's unread count.
func (s *Server) railFor(viewer store.User) rail {
	var rl rail
	rl.Unread = s.st.UnreadNotices(viewer.ID)
	return rl
}
