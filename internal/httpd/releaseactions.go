package httpd

import (
	"fmt"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// Release and build actions. As elsewhere, the browser chooses arguments
// and the command decides: tag existence, access, and the gates around
// archived repositories all stay in one implementation.

func (s *Server) backTo(w http.ResponseWriter, r *http.Request, page, msg string) {
	dest := fmt.Sprintf("/%s/%s/%s", r.PathValue("owner"), r.PathValue("repo"), page)
	s.setFlash(w, msg)
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) releaseSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	tag := strings.TrimSpace(r.FormValue("tag"))
	title := strings.TrimSpace(r.FormValue("title"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	if tag == "" {
		s.backTo(w, r, "releases", "pick a tag")
		return
	}
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "releases", msg) }
	// The CLI's --yes guards against a mistyped tag; here the tag comes from
	// the page and the button sits behind a disclosure, so the click is the
	// deliberate act.
	if r.FormValue("action") == "delete" {
		_, msg, code := s.runControlCode(u, []string{"release", "delete", repo, tag, "--yes"})
		s.done(w, r, code, msg, back)
		return
	}
	verb := "create"
	if r.FormValue("action") == "edit" {
		verb = "edit"
	}
	argv := []string{"release", verb, repo, tag}
	if title != "" {
		argv = append(argv, "--title", title)
	}
	// edit needs at least one field; create takes the tag alone.
	if notes != "" || verb == "edit" {
		argv = append(argv, "--notes", notes)
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}

func (s *Server) buildTriggerSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	job := strings.TrimSpace(r.FormValue("job"))
	if job == "" {
		s.backTo(w, r, "builds", "pick a job")
		return
	}
	_, msg, code := s.runControlCode(u, []string{"build", "trigger", repo, job})
	s.done(w, r, code, msg, func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "builds", msg) })
}

// buildCancelSubmit withdraws a build. The page only offers the control
// while a build is still queued; the command decides for real, so a stale
// page posting against a build that has since finished sees the refusal
// instead of a silent no-op.
func (s *Server) buildCancelSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	n := r.PathValue("n")
	back := func(w http.ResponseWriter, r *http.Request, msg string) {
		s.backTo(w, r, "builds/"+n, msg)
	}
	_, msg, code := s.runControlCode(u, []string{"build", "cancel", repo, n})
	s.done(w, r, code, msg, back)
}
