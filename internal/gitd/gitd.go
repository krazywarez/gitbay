// Package gitd implements the anonymous git:// protocol listener. Read-only
// upload-pack, and only for repositories that are public AND have opted in
// via settings — on an instance where [git_daemon] is enabled at all.
package gitd

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

type Server struct {
	cfg   config.Config
	st    *store.Store
	packs *packlimit.Limiter
}

func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) *Server {
	return &Server{cfg: cfg, st: st, packs: packs}
}

func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	req, err := readPktLine(conn)
	if err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	// Request form: "git-upload-pack /owner/name.git\0host=...\0[\0extra\0]"
	service, rest, ok := strings.Cut(req, " ")
	if !ok || service != "git-upload-pack" {
		writeErr(conn, "only git-upload-pack is available over git://")
		return
	}
	parts := strings.Split(rest, "\x00")
	path := parts[0]
	var protoEnv []string
	for _, p := range parts[1:] {
		if v, ok := strings.CutPrefix(p, "version="); ok {
			protoEnv = []string{"GIT_PROTOCOL=version=" + v}
		}
	}

	repo, err := s.st.RepoByPath(path)
	if err != nil || repo.Visibility != "public" || !repo.Settings.GitDaemon {
		// One answer for missing, private, and not-opted-in.
		writeErr(conn, "repository not exported")
		return
	}

	// A nil done: a queued client that leaves, or a restart, does not
	// end the wait; only the limiter's wait does.
	p := principal(conn.RemoteAddr())
	release, err := s.packs.Acquire(nil, p)
	if err != nil {
		s.packs.Refused("git", p, err)
		writeErr(conn, err.Error())
		return
	}
	// Deferred before git runs, so it fires after git has exited and
	// been waited for.
	defer release()
	out, stalled, unwatch := s.packs.Watch(conn)
	defer unwatch()
	kill := make(chan struct{})
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-finished:
		case <-stalled:
			close(kill)
			// A write blocked on a client that stopped reading outlives
			// git; closing the connection ends it and the stdin copy.
			conn.Close()
		}
	}()

	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	gitutil.Transport("git-upload-pack", dir, conn, out, io.Discard, protoEnv, 0, 0, kill)
}

// principal is the pack-limit principal for a client at addr.
func principal(addr net.Addr) string {
	host, _, _ := net.SplitHostPort(addr.String())
	return packlimit.AddrPrincipal(host)
}

func readPktLine(r io.Reader) (string, error) {
	var lenHex [4]byte
	if _, err := io.ReadFull(r, lenHex[:]); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(lenHex[:]), 16, 16)
	if err != nil || n < 4 || n > 65520 {
		return "", fmt.Errorf("bad pkt length %q", lenHex)
	}
	if n == 4 {
		return "", nil // flush-pkt
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(buf), "\n"), nil
}

func writeErr(w io.Writer, msg string) {
	line := "ERR " + msg + "\n"
	fmt.Fprintf(w, "%04x%s", len(line)+4, line)
}
