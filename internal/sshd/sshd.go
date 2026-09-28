// Package sshd implements the embedded SSH listener: public-key auth against
// registered keys, then dispatch to git transport or control commands.
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/hookd"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

type Server struct {
	cfg         config.Config
	st          *store.Store
	sshCfg      *ssh.ServerConfig
	authLimiter *rateLimiter
	sessions    sync.WaitGroup // accepted connections still being served
	mu          sync.Mutex
	conns       map[*conn]struct{}
	stopping    chan struct{} // closed by Stop
	stopOnce    sync.Once
}

// conn is one accepted connection and how many sessions it is running.
// A CLI's shared connection sits idle between commands; on shutdown an
// idle connection is closed at once and only a session mid-command is
// waited for (#141).
type conn struct {
	net    net.Conn
	active atomic.Int32
	// keyID and userID are the key that authenticated the connection and
	// its account: 0 before the handshake and for an unregistered key.
	// Guarded by Server.mu.
	keyID, userID int64
	revoked       chan struct{} // closed by cut
	cutOnce       sync.Once
}

// cut ends the connection because its key was revoked: a git transport
// on it is killed, and every other command loses its channel.
func (c *conn) cut() {
	c.cutOnce.Do(func() { close(c.revoked) })
	c.net.Close()
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	s := &Server{cfg: cfg, st: st, authLimiter: newRateLimiter(cfg.Limits.SSHAuthRate, time.Minute), conns: map[*conn]struct{}{}, stopping: make(chan struct{})}

	sc := &ssh.ServerConfig{
		PublicKeyCallback: s.authenticate,
		ServerVersion:     "SSH-2.0-gitbayd",
	}
	signers, err := loadHostKeys(cfg)
	if err != nil {
		return nil, err
	}
	for _, sg := range signers {
		sc.AddHostKey(sg)
	}
	s.sshCfg = sc
	st.OnRevoke(s.revoke)
	return s, nil
}

// loadHostKeys loads the configured host keys, or generates an ed25519 key
// under server.root/ssh/ when none are configured.
func loadHostKeys(cfg config.Config) ([]ssh.Signer, error) {
	paths := cfg.SSH.HostKeys
	if len(paths) == 0 {
		p := filepath.Join(cfg.Server.Root, "ssh", "host_ed25519")
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			if err := generateHostKey(p); err != nil {
				return nil, fmt.Errorf("generating host key: %w", err)
			}
			slog.Info("generated ssh host key", "path", p)
		}
		paths = []string{p}
	}
	var signers []ssh.Signer
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("host key %s: %w", p, err)
		}
		sg, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("host key %s: %w", p, err)
		}
		signers = append(signers, sg)
	}
	return signers, nil
}

func generateHostKey(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

// authenticate resolves the presented key to a registered account. The SSH
// username is ignored; identity comes from the key alone. When registration
// is open or invite-based, unknown keys are admitted to run exactly one
// command: register.
func (s *Server) authenticate(meta ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
	ip := remoteIP(meta.RemoteAddr())
	if !s.authLimiter.allow(ip) {
		// One audit entry per throttled window, not per rejected attempt.
		if s.authLimiter.firstThrottle(ip) {
			s.st.Audit(0, "auth.throttled", map[string]any{"ip": ip, "rate": s.cfg.Limits.SSHAuthRate})
		}
		return nil, fmt.Errorf("too many authentication attempts; try again shortly")
	}
	fp := ssh.FingerprintSHA256(pub)
	key, err := s.st.SSHKeyByFingerprint(fp)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		// The store, not the key, failed. Neither a failure against the
		// limiter nor "unknown key": a busy database during a restart
		// would otherwise lock every client out for a minute.
		slog.Error("ssh auth: key lookup", "err", err)
		return nil, fmt.Errorf("authentication temporarily unavailable")
	}
	if err != nil {
		if s.cfg.Registration.Mode != "closed" {
			return &ssh.Permissions{Extensions: map[string]string{
				"anon-key": base64.StdEncoding.EncodeToString(pub.Marshal()),
			}}, nil
		}
		s.authLimiter.fail(ip)
		s.st.Audit(0, "auth.failed", map[string]any{"ip": ip, "fingerprint": fp})
		return nil, fmt.Errorf("unknown key %s", fp)
	}
	if key.Expired(time.Now()) {
		s.authLimiter.fail(ip)
		s.st.Audit(key.UserID, "auth.expired", map[string]any{"ip": ip, "fingerprint": fp})
		return nil, fmt.Errorf("key %s has expired", fp)
	}
	s.authLimiter.success(ip)
	return &ssh.Permissions{Extensions: map[string]string{
		"user-id": strconv.FormatInt(key.UserID, 10),
		"key-id":  strconv.FormatInt(key.ID, 10),
	}}, nil
}

// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	served := make(chan struct{})
	defer close(served)
	go s.sweep(served)
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		c := &conn{net: nc, revoked: make(chan struct{})}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.sessions.Add(1)
		go func() {
			defer s.sessions.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
			s.handleConn(c)
		}()
	}
}

// revoke closes the connections opened by the keys r names.
func (s *Server) revoke(r store.Revoked) {
	var cut []*conn
	s.mu.Lock()
	for c := range s.conns {
		if c.keyID == 0 {
			continue
		}
		if (r.UserID != 0 && c.userID == r.UserID) || slices.Contains(r.KeyIDs, c.keyID) {
			cut = append(cut, c)
		}
	}
	s.mu.Unlock()
	for _, c := range cut {
		c.cut()
	}
}

// sweepInterval bounds how long a revocation this process was not told
// about (gitbayd admin on the host) leaves a connection open.
const sweepInterval = 15 * time.Second

func (s *Server) sweep(served <-chan struct{}) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sweepOnce()
		case <-served:
			return
		case <-s.stopping:
			return
		}
	}
}

// sweepOnce cuts every connection whose key is no longer live. Only
// connections whose key was asked about are judged: one that
// authenticated while the query ran waits for the next sweep.
func (s *Server) sweepOnce() {
	asked := map[int64]bool{}
	s.mu.Lock()
	for c := range s.conns {
		if c.keyID != 0 {
			asked[c.keyID] = true
		}
	}
	s.mu.Unlock()
	if len(asked) == 0 {
		return
	}
	live, err := s.st.LiveSSHKeys(slices.Collect(maps.Keys(asked)))
	if err != nil {
		slog.Error("ssh sweep: key lookup", "err", err)
		return
	}
	var cut []*conn
	s.mu.Lock()
	for c := range s.conns {
		if asked[c.keyID] && !live[c.keyID] {
			cut = append(cut, c)
		}
	}
	s.mu.Unlock()
	for _, c := range cut {
		c.cut()
	}
}

// Stop ends the commands that run until something happens (build log
// --follow), so a shutdown drain waits only for work that finishes. It
// does not close connections; Shutdown does.
func (s *Server) Stop() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

// Shutdown closes every idle connection, then waits for the ones with a
// session running, or for ctx. The caller closes the listener first; a
// push in flight completes rather than being cut mid-pack.
func (s *Server) Shutdown(ctx context.Context) error {
	s.Stop()
	s.mu.Lock()
	for c := range s.conns {
		if c.active.Load() == 0 {
			c.net.Close()
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.sessions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) handleConn(c *conn) {
	defer c.net.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(c.net, s.sshCfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	ext := sconn.Permissions.Extensions
	s.mu.Lock()
	c.keyID, _ = strconv.ParseInt(ext["key-id"], 10, 64)
	c.userID, _ = strconv.ParseInt(ext["user-id"], 10, 64)
	s.mu.Unlock()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		c.active.Add(1)
		go func() {
			defer c.active.Add(-1)
			s.handleSession(c, sconn, ch, chReqs)
		}()
	}
}

func (s *Server) handleSession(c *conn, sconn *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var term control.Term
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			// x/crypto closes reqs when the client closes the channel. That
			// is how a follow learns nobody is reading: the CLI's shared
			// connection outlives a Ctrl-C, the channel does not. Stop
			// ends it too, for a restart.
			closed := make(chan struct{})
			go func() {
				for r := range reqs {
					r.Reply(false, nil)
				}
				close(closed)
			}()
			done := make(chan struct{})
			go func() {
				select {
				case <-closed:
				case <-s.stopping:
				}
				close(done)
			}()
			code := s.runExec(c, sconn, ch, term, payload.Command, done)
			sendExit(ch, code)
			return
		case "shell":
			req.Reply(true, nil)
			fmt.Fprintf(ch, "gitbay control plane: interactive shells are not available.\nTry: ssh %s help\n", s.cfg.Server.SiteURL)
			sendExit(ch, protocol.ExitUsage)
			return
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GITBAY_TERM" {
				term = control.ParseTerm(kv.Value)
			}
			req.Reply(true, nil)
		case "pty-req":
			// Harmless; accept and ignore.
			req.Reply(true, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func sendExit(ch ssh.Channel, code int) {
	var msg = struct{ Status uint32 }{uint32(code)}
	ch.SendRequest("exit-status", false, ssh.Marshal(&msg))
}

func (s *Server) runExec(c *conn, sconn *ssh.ServerConn, ch ssh.Channel, term control.Term, cmdline string, done <-chan struct{}) int {
	ext := sconn.Permissions.Extensions
	if blob := ext["anon-key"]; blob != "" {
		return s.runAnonymous(ch, blob, cmdline)
	}
	userID, _ := strconv.ParseInt(ext["user-id"], 10, 64)
	keyID, _ := strconv.ParseInt(ext["key-id"], 10, 64)
	// A connection outlives its commands, so the key is read again for
	// each one: what it may do is what it may do now (#256).
	key, err := s.st.SSHKeyByID(keyID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && key.UserID != userID) {
		fmt.Fprintln(ch.Stderr(), "this key is no longer registered")
		return protocol.ExitDenied
	}
	if err != nil {
		slog.Error("ssh exec: key lookup", "err", err)
		fmt.Fprintln(ch.Stderr(), "authentication temporarily unavailable")
		return protocol.ExitFailure
	}
	if key.Expired(time.Now()) {
		fmt.Fprintln(ch.Stderr(), "this key has expired; remove it and add a new one")
		return protocol.ExitDenied
	}
	user, err := s.st.UserByID(userID)
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "account no longer exists")
		return protocol.ExitDenied
	}
	_ = s.st.TouchSSHKey(keyID)
	return Exec(s.cfg, s.st, user, key, term, cmdline, ch, ch, ch.Stderr(), done, s.stopping, c.revoked)
}

// runAnonymous handles a session from an unregistered key: the register
// command and nothing else.
func (s *Server) runAnonymous(ch ssh.Channel, keyB64, cmdline string) int {
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return protocol.ExitFailure
	}
	pub, err := ssh.ParsePublicKey(raw)
	if err != nil {
		return protocol.ExitFailure
	}
	argv, err := protocol.Tokenize(cmdline)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "cannot parse command: %v\n", err)
		return protocol.ExitUsage
	}
	if len(argv) == 0 || argv[0] != "register" {
		host := s.cfg.SiteHost()
		fp := ssh.FingerprintSHA256(pub)
		flag := map[string]string{"open": "--email <address>", "invite": "--invite <code>"}[s.cfg.Registration.Mode]
		fmt.Fprintf(ch.Stderr(),
			"this key (%s) is not registered on %s.\n"+
				"already have an account? add it at %s/settings#keys\n"+
				"new here? ssh git@%s register --username <name> %s\n",
			fp, host, strings.TrimSuffix(s.cfg.Server.SiteURL, "/"), host, flag)
		return protocol.ExitDenied
	}
	return control.RunRegister(s.cfg, s.st, pub, argv, ch, ch.Stderr())
}

// Exec runs one SSH exec command line for an authenticated key. It is the
// single dispatch path shared by the embedded listener and the system-sshd
// forced command (gitbayd shell). Closing revoked kills a git transport.
func Exec(cfg config.Config, st *store.Store, user store.User, key store.SSHKey, term control.Term, cmdline string,
	stdin io.Reader, stdout, stderr io.Writer, done, stopping, revoked <-chan struct{}) int {
	if user.Disabled {
		fmt.Fprintln(stderr, "this account is disabled; contact the instance admin")
		return protocol.ExitDenied
	}
	argv, err := protocol.Tokenize(cmdline)
	if err != nil {
		fmt.Fprintf(stderr, "cannot parse command: %v\n", err)
		return protocol.ExitUsage
	}
	if len(argv) > 0 {
		switch argv[0] {
		case "git-upload-pack", "git-receive-pack", "git-upload-archive":
			code := protocol.ExitDenied
			if user.Pending {
				fmt.Fprintln(stderr, "your account is not active yet: verify your email first")
			} else {
				code = runGit(cfg, st, user, key.Scope, argv, stdin, stdout, stderr, revoked)
			}
			// A refused push is a refused write, audited like one. runGit
			// refuses only with the path as the one argument, so argv[1:]
			// holds no value beyond the target.
			if argv[0] == "git-receive-pack" && (code == protocol.ExitDenied || code == protocol.ExitNotFound) {
				control.AuditRefused(st, user.ID, "refused git-receive-pack",
					map[string]any{"argv": argv[1:], "source": key.Fingerprint, "exit": code})
			}
			return code
		case "git-lfs-authenticate":
			// Part of the git transport, not the control plane: usable by
			// git-scoped and deploy keys, with the transports' access rules.
			if user.Pending {
				fmt.Fprintln(stderr, "your account is not active yet: verify your email first")
				return protocol.ExitDenied
			}
			return runLFSAuthenticate(cfg, st, user, key.Scope, argv, stdout, stderr)
		}
	}
	ctx := &control.Ctx{
		User:     user,
		Scope:    key.Scope,
		Source:   key.Fingerprint,
		Term:     term,
		Store:    st,
		Cfg:      cfg,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Done:     done,
		Stopping: stopping,
		Expires:  key.ExpiresAt,
	}
	return control.Dispatch(ctx, argv)
}

// runGit streams a git transport service after access checks.
func runGit(cfg config.Config, st *store.Store, user store.User, scope string, argv []string,
	stdin io.Reader, stdout, stderr io.Writer, revoked <-chan struct{}) int {
	service := argv[0]
	if len(argv) != 2 {
		fmt.Fprintf(stderr, "usage: %s <path>\n", service)
		return protocol.ExitUsage
	}
	write := service == "git-receive-pack"

	repo, err := st.RepoByPath(argv[1])
	if err != nil {
		fmt.Fprintln(stderr, "repository not found")
		return protocol.ExitNotFound
	}
	if policy.IsDeployScope(scope) {
		// A deploy key authorizes by its binding alone: one repository,
		// its mode, nothing inherited from whoever registered it. Any
		// mismatch reads as nonexistence, same as the access rules.
		if !policy.DeployScopeAllows(scope, repo.ID, write) {
			fmt.Fprintln(stderr, "repository not found")
			return protocol.ExitNotFound
		}
	} else {
		grant, err := st.AccessRole(repo.ID, user.ID)
		if err != nil {
			fmt.Fprintln(stderr, "internal error")
			return protocol.ExitFailure
		}
		if !policy.CanRead(user, repo, grant) {
			// Same answer as nonexistence: private repos must not be enumerable.
			fmt.Fprintln(stderr, "repository not found")
			return protocol.ExitNotFound
		}
		if !policy.ScopeAllowsGit(scope, repo.Path(), write) {
			fmt.Fprintf(stderr, "this key's scope (%s) does not allow %s on %s\n", scope, service, repo.Path())
			return protocol.ExitDenied
		}
		if write && !policy.CanWrite(user, repo, grant) {
			fmt.Fprintf(stderr, "write access to %s denied\n", repo.Path())
			return protocol.ExitDenied
		}
	}
	if write && repo.Settings.Archived {
		fmt.Fprintf(stderr, "%s is archived and read-only\n", repo.Path())
		return protocol.ExitDenied
	}
	if write {
		if mirrored, err := st.PullMirrored(repo.ID); err == nil && mirrored {
			fmt.Fprintf(stderr, "%s is a pull mirror: its refs come from the upstream; push there instead\n", repo.Path())
			return protocol.ExitDenied
		}
	}

	dir := control.RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	env := []string{
		hookd.EnvSocket + "=" + hookd.SocketPath(cfg.Server.Root),
		hookd.EnvRepoID + "=" + strconv.FormatInt(repo.ID, 10),
		hookd.EnvUserID + "=" + strconv.FormatInt(user.ID, 10),
		hookd.EnvScope + "=" + scope,
	}
	// A storage quota on the owner rides the same mechanism as the pack
	// cap: the pack may be no larger than what the owner has left.
	maxPack := cfg.Limits.MaxPackBytes
	if write && repo.OwnerKind == "user" {
		if limit := control.ByteLimit(st, control.QuotaConfig(cfg), repo.OwnerID); limit > 0 {
			used := control.OwnedBytes(st, cfg.Server.Root, repo.OwnerID)
			left := limit - used
			if left <= 0 {
				fmt.Fprintf(stderr, "%s's storage quota is used up (%d of %d bytes); delete something, or ask an admin to raise the limit\n", repo.OwnerName, used, limit)
				return protocol.ExitDenied
			}
			if maxPack == 0 || left < maxPack {
				maxPack = left
			}
		}
	}
	if write {
		// hookd answers only a hook that names this receive-pack.
		token, err := st.CreatePushToken(repo.ID, user.ID, scope)
		if err != nil {
			fmt.Fprintln(stderr, "internal error")
			return protocol.ExitFailure
		}
		defer st.DeletePushToken(token)
		env = append(env, hookd.EnvToken+"="+token)
	}
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, env, maxPack, revoked); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}
