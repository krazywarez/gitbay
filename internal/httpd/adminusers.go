package httpd

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// adminUserRow is one account as admin user list returns it. The
// command owns the shape; this is the page's view of it.
type adminUserRow struct {
	Username  string `json:"username"`
	State     string `json:"state"`
	Admin     bool   `json:"admin"`
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
}

// adminUsersPerPage is how many accounts a page shows before offering
// the next, using the command's own keyset cursor so the filter carries
// across pages.
const adminUsersPerPage = 50

// adminUsers is the account list an instance admin manages (#234). It
// dispatches admin user list like any other read, which it can because
// no command is held back from the web any more. A non-admin gets the
// same 404 a missing page would, so the URL confirms nothing.
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if !viewer.IsAdmin {
		s.notFound(w, r)
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "active", "pending", "disabled", "admin":
	default:
		state = "all"
	}
	argv := []string{"admin", "user", "list", "--limit", strconv.Itoa(adminUsersPerPage)}
	if state != "all" {
		argv = append(argv, "--state", state)
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		argv = append(argv, "--cursor", cursor)
	}
	var page struct {
		Items []adminUserRow `json:"items"`
		Next  string         `json:"next"`
	}
	if msg, ok := s.runControlInto(viewer, argv, &page); !ok {
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	next := ""
	if page.Next != "" {
		q := url.Values{"cursor": {page.Next}}
		if state != "all" {
			q.Set("state", state)
		}
		next = "?" + q.Encode()
	}
	s.render(w, "adminusers.html", struct {
		basePage
		Tab    string
		State  string
		Users  []adminUserRow
		Next   string
		Notice string
	}{s.baseFor(viewer), "admin", state, page.Items, next, s.takeFlash(w, r)})
}

// adminUsersSubmit runs one account action. Each is the command an
// admin would run over SSH; demote and disable carry the typed-name
// check, because both take someone's access away and a mistyped row is
// the way that happens by accident. Deletion is not here: it is
// permanent, and it stays a typed command.
func (s *Server) adminUsersSubmit(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if !viewer.IsAdmin {
		s.notFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("user"))
	back := func(msg string) {
		s.setFlash(w, msg)
		dest := "/admin/users"
		if state := r.FormValue("state"); state != "" && state != "all" {
			dest += "?state=" + url.QueryEscape(state)
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
	var verb string
	switch r.FormValue("field") {
	case "promote":
		verb = "promote"
	case "demote":
		verb = "demote"
	case "disable":
		verb = "disable"
	case "enable":
		verb = "enable"
	default:
		back("unknown action")
		return
	}
	if verb == "promote" || verb == "demote" || verb == "disable" {
		if ok, msg := confirmed(r, name); !ok {
			back(msg)
			return
		}
	}
	_, msg, code := s.runControlCode(viewer, []string{"admin", "user", verb, name})
	s.done(w, r, code, msg, func(w http.ResponseWriter, r *http.Request, m string) {
		if m == "" {
			m = name + " " + verb + "d"
		}
		back(m)
	})
}
