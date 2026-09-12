package httpd

import (
	"html/template"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/control"
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
	readable, err := control.ReadableScope(s.st, s.viewer(r), p.Repo)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	labels, err := s.st.ListLabels(p.Repo, readable)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	anyColor := false
	for _, l := range labels {
		if l.Color != "" {
			anyColor = true
			break
		}
	}
	s.render(w, "labels.html", struct {
		repoPage
		Labels      []store.Label
		LabelColors map[string]template.CSS
		CanWrite    bool
		AnyColor    bool
		Notice      string
	}{p, labels, s.labelColors(p.Repo), s.canWriteRepo(r, p.Repo), anyColor, s.takeFlash(w, r)})
}

// labelSubmit creates a label, sets its colour, or removes it, through
// the label commands the CLI runs.
func (s *Server) labelSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	name := strings.TrimSpace(r.FormValue("name"))
	back := func(w http.ResponseWriter, r *http.Request, msg string) { s.backTo(w, r, "labels", msg) }
	if name == "" {
		back(w, r, "name the label")
		return
	}
	argv := []string{"label", "set", repo, name, "--color", strings.TrimSpace(r.FormValue("color"))}
	if r.FormValue("action") == "remove" {
		if ok, msg := confirmed(r, name); !ok {
			s.backTo(w, r, "labels", msg)
			return
		}
		argv = []string{"label", "remove", repo, name}
	}
	_, msg, code := s.runControlCode(u, argv)
	s.done(w, r, code, msg, back)
}
