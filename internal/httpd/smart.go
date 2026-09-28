// Package httpd serves the HTTP listener: anonymous smart-HTTP git reads for
// public repositories, and (from M5) the web UI. There is no authentication
// on this listener by design — private repositories answer 404 everywhere,
// and pushes are refused with a pkt-line ERR so no git version ever falls
// back to asking for credentials.
package httpd

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/toolpath"
)

type Server struct {
	cfg      config.Config
	st       *store.Store
	packs    *packlimit.Limiter
	apiLimit *apiLimiter
	proxies  []*net.IPNet  // http.trusted_proxies, parsed once
	stopping chan struct{} // closed by Stop
	stopOnce sync.Once
}

func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) *Server {
	proxies, _ := cfg.HTTP.TrustedProxyNets() // validated at config load
	return &Server{cfg: cfg, st: st, packs: packs, apiLimit: newAPILimiter(cfg.Limits.APIRate), proxies: proxies,
		stopping: make(chan struct{})}
}

// Stop ends the requests running a command that lasts until something
// happens (build log --follow), so a shutdown drain waits only for work
// that finishes. Other requests, git transport included, run on.
func (s *Server) Stop() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

// until is closed when the request ends or the server stops, whichever
// comes first: the Done a following command runs under.
func (s *Server) until(r *http.Request) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
		case <-s.stopping:
		}
		close(done)
	}()
	return done
}

// receivePackRefusal exists only to fail legibly if a client POSTs without
// reading the advertisement first.
func (s *Server) receivePackRefusal(w http.ResponseWriter, r *http.Request) {
	http.Error(w, s.pushRefusalMessage(r.PathValue("owner"), r.PathValue("repo")), http.StatusForbidden)
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
		cmd := exec.CommandContext(r.Context(), toolpath.Look("git"), "upload-pack", "--stateless-rpc", "--advertise-refs", dir)
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
	br := bufio.NewReader(body)
	cancel := r.Context().Done()
	out := io.Writer(w)
	if !lsRefs(br) {
		o, kill, finish, ok := s.packSlot(w, r)
		if !ok {
			return
		}
		// Deferred before git runs, so it fires after git has exited
		// and been waited for.
		defer finish()
		out, cancel = o, kill
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	cmd := exec.Command(toolpath.Look("git"), "-c", "uploadpack.keepAlive=5", "upload-pack", "--stateless-rpc", dir)
	cmd.Env = append(os.Environ(), gitProtocolEnv(r)...)
	cmd.Stdin = br
	cmd.Stdout = out
	gitutil.RunUntil(cmd, cancel)
}

// packSlot takes a pack-generation slot for r, answering 503 with
// Retry-After when none comes free. A queued request waits at most the
// limiter's wait, and Stop ends the wait so it does not hold up a
// restart's drain; net/http notices a departed client only after the
// body is read, so that rarely ends it. On success out is w watched for
// stalls, kill closes when git must stop — the client left, or no write
// to it completed for packlimit.StallDeadline — and finish, called once
// git has exited, releases the slot.
func (s *Server) packSlot(w http.ResponseWriter, r *http.Request) (out io.Writer, kill <-chan struct{}, finish func(), ok bool) {
	release, err := s.packs.Acquire(s.until(r), s.packPrincipal(r))
	if err != nil {
		msg := "the server is restarting; try again in a minute"
		if errors.Is(err, packlimit.ErrBusy) {
			msg = "the server is busy: it is at its limit of concurrent clones and fetches; try again in a minute"
		}
		w.Header().Set("Retry-After", "30")
		http.Error(w, msg, http.StatusServiceUnavailable)
		return nil, nil, nil, false
	}
	out, stalled, unwatch := s.packs.Watch(w)
	killed := make(chan struct{})
	finished := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-finished:
			return
		case <-r.Context().Done():
		case <-stalled:
			// A write blocked on a client that stopped reading, or a
			// read of a body it stopped sending, outlives git;
			// expired deadlines end both copies, so Wait returns.
			rc := http.NewResponseController(w)
			rc.SetReadDeadline(time.Now())
			rc.SetWriteDeadline(time.Now())
		}
		close(killed)
	}()
	return out, killed, func() {
		// The watcher must not touch w once the handler has returned.
		close(finished)
		<-exited
		unwatch()
		release()
	}, true
}

// lsRefs reports whether a protocol v2 request is a ref listing, which
// generates no pack. Its first pkt-line is "command=ls-refs".
func lsRefs(br *bufio.Reader) bool {
	const want = "command=ls-refs"
	head, err := br.Peek(4 + len(want))
	return err == nil && string(head[4:]) == want
}

// packPrincipal is who a fetch is counted against: the account when the
// request carries a valid bearer token or web session, the same key SSH
// uses, so switching transport buys no extra slots; otherwise the
// client address, an IPv6 one by its /64.
func (s *Server) packPrincipal(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.TrimSpace(tok) != "" {
		if u, _, err := s.st.APITokenUser(store.HashToken(strings.TrimSpace(tok))); err == nil {
			return "user:" + strconv.FormatInt(u.ID, 10)
		}
	}
	if u := s.viewer(r); u.ID != 0 {
		return "user:" + strconv.FormatInt(u.ID, 10)
	}
	return packlimit.AddrPrincipal(s.clientIP(r))
}

// gitProtocolEnv forwards the client's protocol negotiation header so
// protocol v2 works over stateless HTTP.
func gitProtocolEnv(r *http.Request) []string {
	if p := r.Header.Get("Git-Protocol"); p != "" {
		return []string{"GIT_PROTOCOL=" + p}
	}
	return nil
}
