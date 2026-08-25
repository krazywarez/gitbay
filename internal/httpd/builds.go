package httpd

import (
	"net/http"
	"strconv"

	"gitbay.org/gitbay/internal/store"
)

func (s *Server) builds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	builds, _ := s.st.ListBuilds(p.Repo.ID, 50)
	s.render(w, "builds.html", struct {
		repoPage
		Builds []store.Build
	}{p, builds})
}

func (s *Server) build(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	b, err := s.st.BuildByNumber(p.Repo.ID, n)
	if err != nil {
		s.notFound(w, r)
		return
	}
	log, _ := s.st.BuildLog(b.ID)
	s.render(w, "build.html", struct {
		repoPage
		Build store.Build
		Log   string
	}{p, b, string(log)})
}
