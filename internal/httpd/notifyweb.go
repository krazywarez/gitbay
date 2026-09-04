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
// is given, then returns to the list.
func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request, u store.User) {
	var ids []int64
	if v := r.FormValue("id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		ids = append(ids, n)
	}
	if _, err := s.st.MarkNoticesRead(u.ID, ids); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// watchToggle turns watching a repository on and off from its header,
// the way the pin button does.
func (s *Server) watchToggle(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	state := "watching"
	if s.st.RepoWatchState(repo.ID, u.ID) == "watching" {
		state = "muted"
	}
	s.st.SetRepoWatch(repo.ID, u.ID, state)
	http.Redirect(w, r, "/"+repo.Path(), http.StatusSeeOther)
}
