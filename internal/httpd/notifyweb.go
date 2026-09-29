package httpd

import (
	"net/http"
	"strconv"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// noticeView is one inbox row with the pieces the template needs: the
// sigil for its kind and the link, already absolute.
type noticeView struct {
	store.Notice
	Href string
	Read bool
}

// notifications is /notifications: the inbox behind the rail's badge.
// Unread by default; ?all=1 keeps what has been read.
func (s *Server) notifications(w http.ResponseWriter, r *http.Request, u store.User) {
	all := r.URL.Query().Get("all") == "1"
	rows, err := s.st.Inbox(u.ID, !all, 200, 0)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]noticeView, 0, len(rows))
	for _, n := range rows {
		views = append(views, noticeView{n, "/" + n.Path, n.ReadAt != ""})
	}
	s.render(w, "notifications.html", struct {
		basePage
		Tab     string
		All     bool
		Notices []noticeView
	}{s.baseFor(u), "notifications", all, views})
}

// notificationsRead marks one notice read, or the whole inbox when no id
// is given, through notifications read, then returns to the list (#261).
func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request, u store.User) {
	argv := []string{"notifications", "read", "--all"}
	if v := r.FormValue("id"); v != "" {
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		argv = []string{"notifications", "read", v}
	}
	if _, msg, ok := s.runControl(u, argv); !ok {
		s.setFlash(w, msg)
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// watchToggle cycles the viewer's watch state on a repository: default,
// watching, muted, back to default — through repo watch/repo mute/repo
// unwatch, the same commands the CLI runs (#261, #271).
func (s *Server) watchToggle(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	next := map[string]string{"": "watch", "watching": "mute", "muted": "unwatch"}
	verb := next[s.st.RepoWatchState(repo.ID, u.ID)]
	if _, msg, ok := s.runControl(u, []string{"repo", verb, repo.Path()}); !ok {
		s.setFlash(w, msg)
	}
	http.Redirect(w, r, "/"+repo.Path(), http.StatusSeeOther)
}
