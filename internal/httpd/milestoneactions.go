package httpd

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// Milestone, org label and release asset forms. Each dispatches the
// command the CLI runs; who may do it is the command's decision, and the
// pages only show the forms to those it will accept.

// milestoneCreateArgs builds the argv tail for create: the title, and the
// description and due date when given.
func milestoneCreateArgs(r *http.Request, head []string) []string {
	argv := append(head, strings.TrimSpace(r.FormValue("title")))
	if d := strings.TrimSpace(r.FormValue("description")); d != "" {
		argv = append(argv, "--description", d)
	}
	if d := strings.TrimSpace(r.FormValue("due")); d != "" {
		argv = append(argv, "--due", d)
	}
	return argv
}

func (s *Server) milestoneSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "milestones", msg) }
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		back(w, r, "name the milestone")
		return
	}
	var argv []string
	switch r.FormValue("action") {
	case "close":
		argv = []string{"milestone", "close", repo, title}
	case "reopen":
		argv = []string{"milestone", "reopen", repo, title}
	default:
		argv = milestoneCreateArgs(r, []string{"milestone", "create", repo})
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}

func (s *Server) orgBack(w http.ResponseWriter, r *http.Request, page, msg string) {
	s.setFlash(w, msg)
	http.Redirect(w, r, "/"+r.PathValue("owner")+"/-/"+page, http.StatusSeeOther)
}

func (s *Server) orgLabelSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	org := r.PathValue("owner")
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.orgBack(w, r, "labels", msg) }
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		back(w, r, "name the label")
		return
	}
	argv := []string{"org", "label", "set", org, name, "--color", strings.TrimSpace(r.FormValue("color"))}
	if r.FormValue("action") == "remove" {
		if ok, msg := confirmed(r, name); !ok {
			back(w, r, msg)
			return
		}
		argv = []string{"org", "label", "remove", org, name}
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}

func (s *Server) orgMilestoneSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	org := r.PathValue("owner")
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.orgBack(w, r, "milestones", msg) }
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		back(w, r, "name the milestone")
		return
	}
	var argv []string
	switch r.FormValue("action") {
	case "close":
		argv = []string{"org", "milestone", "close", org, title}
	case "reopen":
		argv = []string{"org", "milestone", "reopen", org, title}
	default:
		argv = milestoneCreateArgs(r, []string{"org", "milestone", "create", org})
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}

// releaseAssetSubmit uploads or removes a release asset. The upload is
// parsed with a small memory budget, so the rest of the file spills to a
// temporary file, and reaches release asset add as a stream on stdin.
// The body is capped a little above max_asset_bytes so an oversized file
// is refused without being read to the end; the command applies the
// exact limit.
func (s *Server) releaseAssetSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "releases", msg) }
	limit := s.cfg.Limits.MaxAssetBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			back(w, r, fmt.Sprintf("asset exceeds max_asset_bytes (%d)", limit))
			return
		}
		back(w, r, "unreadable upload")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	tag := strings.TrimSpace(r.FormValue("tag"))
	if tag == "" {
		back(w, r, "pick a release")
		return
	}
	if r.FormValue("action") == "remove" {
		name := strings.TrimSpace(r.FormValue("name"))
		if ok, msg := confirmed(r, name); !ok || name == "" {
			if name == "" {
				msg = "name the asset"
			}
			back(w, r, msg)
			return
		}
		_, msg, code := s.runControlCode(u, []string{"release", "asset", "remove", repo, tag, name})
		s.done(w, r, code, msg, back)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		back(w, r, "choose a file")
		return
	}
	defer f.Close()
	if hdr.Size == 0 {
		back(w, r, "the file is empty")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = filepath.Base(hdr.Filename)
	}
	msg, code := s.runControlReader(u, []string{"release", "asset", "add", repo, tag, name}, f)
	s.done(w, r, code, msg, back)
}
