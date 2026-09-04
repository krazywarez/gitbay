package httpd

import (
	"net/http"
	"strconv"

	"gitbay.org/gitbay/internal/control"
)

func (s *Server) builds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	viewer := s.webViewer(r)

	var builds []control.BuildOut
	s.runControlInto(viewer, []string{"build", "list", p.Repo.Path()}, &builds)

	// The jobs a trigger can name. A repo without a CI config has none;
	// that is not an error for this page.
	var jobs []control.JobOut
	s.runControlInto(viewer, []string{"build", "jobs", p.Repo.Path()}, &jobs)

	s.render(w, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		CanWrite bool
		Notice   string
	}{p, builds, jobs, s.canWriteRepo(r, p.Repo), s.takeFlash(w, r)})
}

func (s *Server) build(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	if _, err := strconv.ParseInt(r.PathValue("n"), 10, 64); err != nil {
		s.notFound(w, r)
		return
	}
	n := r.PathValue("n")
	viewer := s.webViewer(r)

	var b control.BuildOut
	if _, ok := s.runControlInto(viewer, []string{"build", "show", p.Repo.Path(), n}, &b); !ok {
		s.notFound(w, r)
		return
	}
	log, _, _ := s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})

	s.render(w, "build.html", struct {
		repoPage
		Build control.BuildOut
		Log   string
	}{p, b, log})
}
