package httpd

import (
	"html/template"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// labels lists a repository's label set with its colour and how many
// issues carry it. Applying a label is on the issue page; this is where
// the set itself is kept (#163).
func (s *Server) labels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "issues"
	labels, err := s.st.ListLabels(p.Repo.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "labels.html", struct {
		repoPage
		Labels      []store.Label
		LabelColors map[string]template.CSS
		CanWrite    bool
		Notice      string
	}{p, labels, s.labelColors(p.Repo.ID), s.canWriteRepo(r, p.Repo), s.takeFlash(w, r)})
}

// labelSubmit creates a label, sets its colour, or removes it, through
// the label commands the CLI runs.
func (s *Server) labelSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	name := strings.TrimSpace(r.FormValue("name"))
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "labels", msg) }
	if name == "" {
		s.backTo(w, r, "labels", "name the label")
		return
	}
	argv := []string{"label", "set", repo, name, "--color", strings.TrimSpace(r.FormValue("color"))}
	if r.FormValue("action") == "remove" {
		argv = []string{"label", "remove", repo, name}
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}
