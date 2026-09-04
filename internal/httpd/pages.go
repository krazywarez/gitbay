package httpd

import (
	"mime"
	"net"
	"net/http"
	"net/url"
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
	siteHost := s.cfg.SiteHost()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if domain != "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
			s.servePage(w, r, host)
			return
		}
		// Any other foreign host may be a custom pages domain.
		if host != siteHost && host != "" {
			if repo, err := s.st.PageDomainRepo(host); err == nil && repo.Visibility == "public" {
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				s.servePageFile(w, r, repo, strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/"))
				return
			}
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
					http.Redirect(w, r, pageRedirectTarget(r.URL.Path), http.StatusMovedPermanently)
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
				http.Redirect(w, r, pageRedirectTarget(r.URL.Path), http.StatusMovedPermanently)
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

// pageRedirectTarget is the "add a trailing slash" destination for a
// directory URL, normalised so it cannot leave the site.
//
// The raw request path is not safe to redirect to. net/url keeps a
// leading "//", and Go emits `Location: //evil.example/` unchanged, which
// a browser reads as protocol-relative and follows to another origin.
// Reaching it needed content named like a host under the subdomain's
// owner — repository names permit dots — so it was narrow rather than
// impossible (gosecurity:S5146, #153).
//
// path.Clean collapses the leading slashes and resolves any "..", and the
// result is re-rooted, so the destination is always one same-origin
// absolute path.
func pageRedirectTarget(reqPath string) string {
	clean := path.Clean("/" + reqPath)
	if clean == "/" {
		return "/"
	}
	// Encoding through url.URL rather than concatenating: a backslash is
	// not a path separator here but browsers following the WHATWG URL
	// rules treat one as a slash, so "/\\evil.example/" would be another
	// way to say "//evil.example/". Escaping settles that, and every other
	// byte a path can hold, without a denylist.
	return (&url.URL{Path: clean + "/"}).String()
}
