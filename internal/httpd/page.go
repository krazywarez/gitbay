package httpd

import (
	"net/http"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// railRepo is one pinned repository in the rail.
type railRepo struct {
	Owner string
	Name  string
}

// railItem is one merge request in the rail's review queue.
type railItem struct {
	RepoPath string
	Number   int64
	Title    string
}

// rail is the cross-repo state in the left rail: the repositories and the
// review work a viewer moves between. A repo's own navigation lives in its
// header tabs, not here — the rail holds only what changes as you switch
// context, which is what earns it the width.
type rail struct {
	Pinned  []railRepo
	Reviews []railItem
	Unread  int
}

// Empty reports whether the rail has nothing to show beyond the global
// links, so the template can skip its group headings.
func (r rail) Empty() bool { return len(r.Pinned) == 0 && len(r.Reviews) == 0 }

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
	return b
}

// railFor collects the viewer's pinned repositories and review queue,
// dropping anything they may no longer read.
func (s *Server) railFor(viewer store.User) rail {
	var rl rail
	pinned, _ := s.st.PinnedRepos(viewer.ID)
	for _, rp := range pinned {
		grant, _ := s.st.AccessRole(rp.ID, viewer.ID)
		if policy.CanRead(viewer, rp, grant) {
			rl.Pinned = append(rl.Pinned, railRepo{Owner: rp.OwnerName, Name: rp.Name})
		}
	}
	rl.Unread = s.st.UnreadNotices(viewer.ID)
	queue, _ := s.st.ReviewQueue(viewer.ID)
	for _, q := range queue {
		rl.Reviews = append(rl.Reviews, railItem{
			RepoPath: q.RepoPath,
			Number:   q.Number,
			Title:    q.Title,
		})
	}
	return rl
}
