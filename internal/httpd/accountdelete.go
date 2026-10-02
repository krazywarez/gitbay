package httpd

import (
	"net/http"
	"time"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// accountDeletePage is where the mailed deletion link lands. The token
// is the authority, as a login link's is, so no session is needed; like
// the login token it rides the query string. The GET changes nothing; the
// button posts back to the same URL.
type accountDeletePage struct {
	basePage
	User      string
	Scheduled string
}

func (s *Server) accountDeleteForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	page := accountDeletePage{basePage: s.base(r)}
	if u, err := s.st.AccountDeletionUser(store.HashToken(r.URL.Query().Get("token"))); err == nil {
		page.User = u.Username
	}
	s.render(w, "accountdelete.html", page)
}

func (s *Server) accountDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := s.st.ConfirmAccountDeletion(store.HashToken(r.URL.Query().Get("token")), time.Now().Add(control.DeletionGrace))
	if err != nil {
		s.render(w, "accountdelete.html", accountDeletePage{basePage: s.base(r)})
		return
	}
	s.st.Audit(u.ID, "account.delete.scheduled", map[string]any{"user": u.Username, "after": u.DeleteAfter})
	http.SetCookie(w, s.clearCookie(sessionCookie, sessionSameSite))
	s.render(w, "accountdelete.html", accountDeletePage{basePage: s.base(r), User: u.Username, Scheduled: u.DeleteAfter})
}
