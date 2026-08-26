package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// Merge request actions. Each one runs the control command the CLI runs,
// so review rules, merge gates, and audit entries have a single
// implementation; the browser only chooses arguments and shows the
// result.

// mrRedirect returns to the merge request, carrying a failure message the
// page renders as a banner.
func (s *Server) mrRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	dest := fmt.Sprintf("/%s/%s/mrs/%s",
		r.PathValue("owner"), r.PathValue("repo"), r.PathValue("n"))
	if msg != "" {
		if len(msg) > 300 {
			msg = msg[:300]
		}
		dest += "?e=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// mrArgs builds "<verb> owner/name <n>" for the mr command family.
func mrArgs(r *http.Request, verb string, extra ...string) []string {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	return append([]string{"mr", verb, repo, r.PathValue("n")}, extra...)
}

func (s *Server) mrReviewSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	flag := map[string]string{
		"approve":         "--approve",
		"request-changes": "--request-changes",
		"comment":         "--comment",
	}[r.FormValue("verdict")]
	if flag == "" {
		s.mrRedirect(w, r, "pick approve, request changes, or comment")
		return
	}
	_, msg, ok := s.runControl(u, mrArgs(r, "review", flag))
	if ok {
		msg = ""
	}
	s.mrRedirect(w, r, msg)
}

func (s *Server) mrMergeSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	args := []string{}
	if st := strings.TrimSpace(r.FormValue("strategy")); st != "" && st != "auto" {
		args = append(args, "--strategy", st)
	}
	_, msg, ok := s.runControl(u, mrArgs(r, "merge", args...))
	if ok {
		msg = ""
	}
	s.mrRedirect(w, r, msg)
}

func (s *Server) mrCloseSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	_, msg, ok := s.runControl(u, mrArgs(r, "close"))
	if ok {
		msg = ""
	}
	s.mrRedirect(w, r, msg)
}

// mrThreadSubmit resolves or reopens one review thread.
func (s *Server) mrThreadSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	verb := "resolve"
	if r.FormValue("action") == "unresolve" {
		verb = "unresolve"
	}
	id := strings.TrimSpace(r.FormValue("thread"))
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		s.mrRedirect(w, r, "bad thread id")
		return
	}
	_, msg, ok := s.runControl(u, mrArgs(r, verb, id))
	if ok {
		msg = ""
	}
	s.mrRedirect(w, r, msg)
}
