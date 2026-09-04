package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// Release and build actions. As elsewhere, the browser chooses arguments
// and the command decides: tag existence, access, and the gates around
// archived repositories all stay in one implementation.

func (s *Server) backTo(w http.ResponseWriter, r *http.Request, page, msg string) {
	dest := fmt.Sprintf("/%s/%s/%s", r.PathValue("owner"), r.PathValue("repo"), page)
	if msg != "" {
		if len(msg) > 300 {
			msg = msg[:300]
		}
		dest += "?e=" + url.QueryEscape(msg)
	}
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
	s.done(w, r, code, msg, func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "releases", msg) })
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
