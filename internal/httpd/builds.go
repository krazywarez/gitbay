package httpd

import (
	"net/http"
	"strconv"
)

// buildView mirrors the build commands' JSON. The templates read these
// names; nothing here touches the store.
type buildView struct {
	Number     int64  `json:"number"`
	Job        string `json:"job"`
	Status     string `json:"status"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at"`
}

type jobView struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Tags     string `json:"tags"`
}

func (s *Server) builds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	viewer := s.webViewer(r)

	var builds []buildView
	s.runControlInto(viewer, []string{"build", "list", p.Repo.Path()}, &builds)

	// The jobs a trigger can name. A repo without a CI config has none;
	// that is not an error for this page.
	var jobs []jobView
	s.runControlInto(viewer, []string{"build", "jobs", p.Repo.Path()}, &jobs)

	s.render(w, "builds.html", struct {
		repoPage
		Builds   []buildView
		Jobs     []jobView
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

	var b buildView
	if _, ok := s.runControlInto(viewer, []string{"build", "show", p.Repo.Path(), n}, &b); !ok {
		s.notFound(w, r)
		return
	}
	log, _, _ := s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})

	s.render(w, "build.html", struct {
		repoPage
		Build buildView
		Log   string
	}{p, b, log})
}
