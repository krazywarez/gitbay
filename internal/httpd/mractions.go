package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
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
	s.setFlash(w, msg)
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// mrDiffRedirect returns to the diff view, where the thread controls are.
func (s *Server) mrDiffRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	dest := fmt.Sprintf("/%s/%s/mrs/%s?view=diff",
		r.PathValue("owner"), r.PathValue("repo"), r.PathValue("n"))
	s.setFlash(w, msg)
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
	_, msg, code := s.runControlCode(u, mrArgs(r, "review", flag))
	s.done(w, r, code, msg, s.mrRedirect)
}

func (s *Server) mrMergeSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	args := []string{}
	if st := strings.TrimSpace(r.FormValue("strategy")); st != "" && st != "auto" {
		args = append(args, "--strategy", st)
	}
	_, msg, code := s.runControlCode(u, mrArgs(r, "merge", args...))
	s.done(w, r, code, msg, s.mrRedirect)
}

func (s *Server) mrCloseSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	_, msg, code := s.runControlCode(u, mrArgs(r, "close"))
	s.done(w, r, code, msg, s.mrRedirect)
}

// mrDiffCommentSubmit opens a review thread on a diff line, or replies to
// one. The body goes in on stdin: it is user prose, and argv is visible in
// /proc.
func (s *Server) mrDiffCommentSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		s.mrDiffRedirect(w, r, "empty comment")
		return
	}
	var extra []string
	if reply := strings.TrimSpace(r.FormValue("reply")); reply != "" {
		if _, err := strconv.ParseInt(reply, 10, 64); err != nil {
			s.mrDiffRedirect(w, r, "bad thread id")
			return
		}
		extra = []string{"--reply", reply}
	} else {
		path := strings.TrimSpace(r.FormValue("path"))
		line := strings.TrimSpace(r.FormValue("line"))
		if n, err := strconv.ParseInt(line, 10, 64); path == "" || err != nil || n < 1 {
			s.mrDiffRedirect(w, r, "pick a line to comment on")
			return
		}
		extra = []string{"--path", path, "--line", line}
		if r.FormValue("side") == "old" {
			extra = append(extra, "--old")
		}
	}
	msg, code := s.runControlStdinCode(u, mrArgs(r, "diff-comment", append(extra, "--file", "-")...), body)
	s.done(w, r, code, msg, s.mrDiffRedirect)
}

// mrRetargetSubmit moves the merge request onto another branch.
func (s *Server) mrRetargetSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		s.mrRedirect(w, r, "pick a branch to retarget onto")
		return
	}
	_, msg, code := s.runControlCode(u, mrArgs(r, "retarget", target))
	s.done(w, r, code, msg, s.mrRedirect)
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
	_, msg, code := s.runControlCode(u, mrArgs(r, verb, id))
	s.done(w, r, code, msg, s.mrRedirect)
}

// mrNewPage is the create form: branches to choose from, plus whatever
// the last attempt had in it so a refusal does not lose the draft.
type mrNewPage struct {
	repoPage
	Branches []gitutil.Ref
	Source   string
	Target   string
	Title    string
	Body     string
	Notice   string
}

func (s *Server) mrCreateForm(w http.ResponseWriter, r *http.Request, u store.User) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "merge requests"
	branches, _ := gitutil.Refs(p.Dir, "heads")
	q := r.URL.Query()
	target := q.Get("target")
	if target == "" {
		target = p.Repo.DefaultBranch
	}
	s.render(w, "mrnew.html", mrNewPage{
		repoPage: p, Branches: branches,
		Source: q.Get("source"), Target: target,
		Title: q.Get("title"), Body: q.Get("body"), Notice: s.takeFlash(w, r),
	})
}

func (s *Server) mrCreateSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	source := strings.TrimSpace(r.FormValue("source"))
	target := strings.TrimSpace(r.FormValue("target"))
	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))

	back := func(msg string) {
		q := url.Values{"source": {source}, "target": {target}, "title": {title}, "body": {body}}
		s.setFlash(w, msg)
		http.Redirect(w, r, fmt.Sprintf("/%s/mrs/new?%s", p.Repo.Path(), q.Encode()), http.StatusSeeOther)
	}
	if source == "" || title == "" {
		back("pick a source branch and give the merge request a title")
		return
	}
	argv := []string{"mr", "create", p.Repo.Path(), "--source", source, "--title", title}
	if target != "" {
		argv = append(argv, "--target", target)
	}
	if body != "" {
		argv = append(argv, "--body", body)
	}
	var created control.MRCreated
	if msg, ok := s.runControlInto(u, argv, &created); !ok {
		back(msg)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/mrs/%d", p.Repo.Path(), created.Number), http.StatusSeeOther)
}
