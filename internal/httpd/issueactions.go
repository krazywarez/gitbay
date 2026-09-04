package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// Issue actions. Like the merge request controls, each one runs the
// command the CLI runs, so label rules, access checks, and the system
// comments they leave behind have a single implementation.

func (s *Server) issueRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	dest := fmt.Sprintf("/%s/%s/issues/%s",
		r.PathValue("owner"), r.PathValue("repo"), r.PathValue("n"))
	if msg != "" {
		if len(msg) > 300 {
			msg = msg[:300]
		}
		dest += "?e=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func issueArgs(r *http.Request, verb string, extra ...string) []string {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	return append([]string{"issue", verb, repo, r.PathValue("n")}, extra...)
}

// issueStateSubmit closes or reopens, following the button pressed.
func (s *Server) issueStateSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	verb := "close"
	if r.FormValue("action") == "reopen" {
		verb = "reopen"
	}
	_, msg, code := s.runControlCode(u, issueArgs(r, verb))
	s.done(w, r, code, msg, s.issueRedirect)
}

// fieldArgs turns a space-separated form value into repeated flags, the
// shape the label and assign commands take.
func fieldArgs(flag, values string) []string {
	var out []string
	for _, v := range strings.Fields(values) {
		out = append(out, flag, v)
	}
	return out
}

func (s *Server) issueLabelSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	args := append(fieldArgs("--add", r.FormValue("add")), fieldArgs("--remove", r.FormValue("remove"))...)
	if len(args) == 0 {
		s.issueRedirect(w, r, "name at least one label")
		return
	}
	_, msg, code := s.runControlCode(u, issueArgs(r, "label", args...))
	s.done(w, r, code, msg, s.issueRedirect)
}

func (s *Server) issueAssignSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	args := append(fieldArgs("--add", r.FormValue("add")), fieldArgs("--remove", r.FormValue("remove"))...)
	if len(args) == 0 {
		s.issueRedirect(w, r, "name at least one person")
		return
	}
	_, msg, code := s.runControlCode(u, issueArgs(r, "assign", args...))
	s.done(w, r, code, msg, s.issueRedirect)
}

func (s *Server) issueMilestoneSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	title := strings.TrimSpace(r.FormValue("milestone"))
	if title == "" {
		title = "none"
	}
	_, msg, code := s.runControlCode(u, issueArgs(r, "milestone", title))
	s.done(w, r, code, msg, s.issueRedirect)
}
