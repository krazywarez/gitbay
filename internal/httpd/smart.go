// Package httpd serves the HTTP listener: anonymous smart-HTTP git reads for
// public repositories, and (from M5) the web UI. There is no authentication
// on this listener by design — private repositories answer 404 everywhere,
// and pushes are refused with a pkt-line ERR so no git version ever falls
// back to asking for credentials.
package httpd

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/krazywarez/forge/internal/config"
	"github.com/krazywarez/forge/internal/control"
	"github.com/krazywarez/forge/internal/store"
)

type Server struct {
	cfg config.Config
	st  *store.Store
}

func New(cfg config.Config, st *store.Store) *Server {
	return &Server{cfg: cfg, st: st}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", s.infoRefs)
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", s.uploadPack)
	// Push endpoints exist only to fail legibly.
	mux.HandleFunc("POST /{owner}/{repo}/git-receive-pack", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, s.pushRefusalMessage(r.PathValue("owner"), r.PathValue("repo")), http.StatusForbidden)
	})
	return mux
}

// publicRepo resolves owner/name and returns it only if it exists and is
// public. Every failure mode is the same 404.
func (s *Server) publicRepo(owner, name string) (store.Repo, bool) {
	repo, err := s.st.RepoByPath(owner + "/" + name)
	if err != nil || repo.Visibility != "public" {
		return store.Repo{}, false
	}
	return repo, true
}

func pktLine(w io.Writer, s string) {
	fmt.Fprintf(w, "%04x%s", len(s)+4, s)
}

func pktFlush(w io.Writer) { io.WriteString(w, "0000") }

func (s *Server) pushRefusalMessage(owner, repo string) string {
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s.cfg.Server.SiteURL, "https://"), "http://"), "/")
	name := strings.TrimSuffix(repo, ".git")
	return fmt.Sprintf("pushes to this forge go over SSH: git remote set-url --push origin git@%s:%s/%s.git", host, owner, name)
}

func (s *Server) infoRefs(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	repo, ok := s.publicRepo(owner, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch service := r.URL.Query().Get("service"); service {
	case "git-upload-pack":
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.Header().Set("Cache-Control", "no-cache")
		pktLine(w, "# service=git-upload-pack\n")
		pktFlush(w)
		dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
		cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", "--advertise-refs", dir)
		cmd.Env = append(os.Environ(), gitProtocolEnv(r)...)
		cmd.Stdout = w
		cmd.Run()
	case "git-receive-pack":
		// HTTP 200 with a pkt-line ERR: every git version renders this as
		// "fatal: remote error: ..." and never falls back to credential
		// prompting the way a 401/403 would.
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		w.Header().Set("Cache-Control", "no-cache")
		pktLine(w, "# service=git-receive-pack\n")
		pktFlush(w)
		pktLine(w, "ERR "+s.pushRefusalMessage(owner, name)+"\n")
	default:
		// Dumb-protocol clients are not supported.
		http.NotFound(w, r)
	}
}

func (s *Server) uploadPack(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.publicRepo(r.PathValue("owner"), r.PathValue("repo"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "bad gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", dir)
	cmd.Env = append(os.Environ(), gitProtocolEnv(r)...)
	cmd.Stdin = body
	cmd.Stdout = w
	cmd.Run()
}

// gitProtocolEnv forwards the client's protocol negotiation header so
// protocol v2 works over stateless HTTP.
func gitProtocolEnv(r *http.Request) []string {
	if p := r.Header.Get("Git-Protocol"); p != "" {
		return []string{"GIT_PROTOCOL=" + p}
	}
	return nil
}
