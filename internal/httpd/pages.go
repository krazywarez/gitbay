package httpd

import (
	"mime"
	"net"
	"net/http"
	"path"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// PagesBranch is the branch a repo publishes as its static site.
const PagesBranch = "refs/heads/pages"

// pagesRouter sends <owner>.<domain> requests to the pages server and
// everything else to the forge. Pages responses deliberately bypass the
// forge's security headers: sites need their own scripts, and they run on
// a separate origin where the forge has no cookies to protect.
func (s *Server) pagesRouter(forge http.Handler) http.Handler {
	domain := s.cfg.Pages.Domain
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if host == domain || strings.HasSuffix(host, "."+domain) {
			s.servePage(w, r, host)
			return
		}
		forge.ServeHTTP(w, r)
	})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// servePage maps <owner>.<domain>/<repo>/<path> to the repo's pages
// branch, and <owner>.<domain>/<path> to the owner's repo named "pages".
// Private repos and missing branches are plain 404s.
func (s *Server) servePage(w http.ResponseWriter, r *http.Request, host string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The apex has no site of its own; send visitors to the forge.
	if host == s.cfg.Pages.Domain {
		http.Redirect(w, r, s.cfg.Server.SiteURL, http.StatusFound)
		return
	}
	owner, ok := strings.CutSuffix(host, "."+s.cfg.Pages.Domain)
	if !ok || owner == "" || strings.Contains(owner, ".") {
		http.NotFound(w, r)
		return
	}
	reqPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

	// A first segment naming a public repo with a pages branch wins;
	// everything else falls through to the owner's "pages" repo.
	if seg, rest, _ := strings.Cut(reqPath, "/"); seg != "" && seg != "pages" {
		if repo, err := s.st.RepoByPath(owner + "/" + seg); err == nil && repo.Visibility == "public" {
			dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
			if _, err := gitutil.ResolveRef(dir, PagesBranch); err == nil {
				if rest == "" && !strings.HasSuffix(r.URL.Path, "/") {
					http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
					return
				}
				s.servePageFile(w, r, repo, rest)
				return
			}
		}
	}
	repo, err := s.st.RepoByPath(owner + "/pages")
	if err != nil || repo.Visibility != "public" {
		http.NotFound(w, r)
		return
	}
	s.servePageFile(w, r, repo, reqPath)
}

func (s *Server) servePageFile(w http.ResponseWriter, r *http.Request, repo store.Repo, filePath string) {
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	if filePath == "" {
		filePath = "index.html"
	}
	data, err := gitutil.ReadBlob(dir, PagesBranch, filePath, s.cfg.Limits.MaxBlobBytes)
	if err != nil {
		// A directory path serves its index.html; /guide -> /guide/ keeps
		// relative links working.
		if idx, ierr := gitutil.ReadBlob(dir, PagesBranch, filePath+"/index.html", s.cfg.Limits.MaxBlobBytes); ierr == nil {
			if !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
				return
			}
			data, filePath = idx, filePath+"/index.html"
		} else {
			http.NotFound(w, r)
			return
		}
	}
	ct := mime.TypeByExtension(path.Ext(filePath))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=60")
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}
