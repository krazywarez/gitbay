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
	"net"
	"os"
	"path/filepath"
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
}

// conn is one accepted connection and how many sessions it is running.
// A CLI's shared connection sits idle between commands; on shutdown an
// idle connection is closed at once and only a session mid-command is
// waited for (#141).
type conn struct {
	net    net.Conn
	active atomic.Int32
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	s := &Server{cfg: cfg, st: st, authLimiter: newRateLimiter(cfg.Limits.SSHAuthRate, time.Minute), conns: map[*conn]struct{}{}}

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
	s.authLimiter.success(ip)
	return &ssh.Permissions{Extensions: map[string]string{
		"user-id": strconv.FormatInt(key.UserID, 10),
		"key-id":  strconv.FormatInt(key.ID, 10),
		"key-fp":  fp,
		"scope":   key.Scope,
	}}, nil
}

// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		c := &conn{net: nc}
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

// Shutdown closes every idle connection, then waits for the ones with a
// session running, or for ctx. The caller closes the listener first; a
// push in flight completes rather than being cut mid-pack.
func (s *Server) Shutdown(ctx context.Context) error {
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
			s.handleSession(sconn, ch, chReqs)
		}()
	}
}

func (s *Server) handleSession(sconn *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			code := s.runExec(sconn, ch, payload.Command)
			sendExit(ch, code)
			return
		case "shell":
			req.Reply(true, nil)
			fmt.Fprintf(ch, "gitbay control plane: interactive shells are not available.\nTry: ssh %s help\n", s.cfg.Server.SiteURL)
			sendExit(ch, protocol.ExitUsage)
			return
		case "pty-req", "env":
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

func (s *Server) runExec(sconn *ssh.ServerConn, ch ssh.Channel, cmdline string) int {
	ext := sconn.Permissions.Extensions
	if blob := ext["anon-key"]; blob != "" {
		return s.runAnonymous(ch, blob, cmdline)
	}
	userID, _ := strconv.ParseInt(ext["user-id"], 10, 64)
	keyID, _ := strconv.ParseInt(ext["key-id"], 10, 64)
	user, err := s.st.UserByID(userID)
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "account no longer exists")
		return protocol.ExitDenied
	}
	_ = s.st.TouchSSHKey(keyID)
	return Exec(s.cfg, s.st, user, ext["scope"], ext["key-fp"], cmdline, ch, ch, ch.Stderr())
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
		fmt.Fprintf(ch.Stderr(), "this key is not registered here. Create an account with:\n  ssh <host> register --username <name> %s\n",
			map[string]string{"open": "--email <address>", "invite": "--invite <code>"}[s.cfg.Registration.Mode])
		return protocol.ExitDenied
	}
	return control.RunRegister(s.cfg, s.st, pub, argv, ch, ch.Stderr())
}

// Exec runs one SSH exec command line for an authenticated key. It is the
// single dispatch path shared by the embedded listener and the system-sshd
// forced command (gitbayd shell).
func Exec(cfg config.Config, st *store.Store, user store.User, scope, source, cmdline string,
	stdin io.Reader, stdout, stderr io.Writer) int {
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
			if user.Pending {
				fmt.Fprintln(stderr, "your account is not active yet: verify your email first")
				return protocol.ExitDenied
			}
			return runGit(cfg, st, user, scope, argv, stdin, stdout, stderr)
		case "git-lfs-authenticate":
			// Part of the git transport, not the control plane: usable by
			// git-scoped and deploy keys, with the transports' access rules.
			if user.Pending {
				fmt.Fprintln(stderr, "your account is not active yet: verify your email first")
				return protocol.ExitDenied
			}
			return runLFSAuthenticate(cfg, st, user, scope, argv, stdout, stderr)
		}
	}
	ctx := &control.Ctx{
		User:   user,
		Scope:  scope,
		Source: source,
		Store:  st,
		Cfg:    cfg,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}
	return control.Dispatch(ctx, argv)
}

// runGit streams a git transport service after access checks.
func runGit(cfg config.Config, st *store.Store, user store.User, scope string, argv []string,
	stdin io.Reader, stdout, stderr io.Writer) int {
	service := argv[0]
	if len(argv) != 2 {
		fmt.Fprintf(stderr, "usage: %s <path>\n", service)
		return protocol.ExitUsage
	}
	write := service == "git-receive-pack"

	// Wiki companion repos: ssh://.../owner/name.wiki.git. Access derives
	// from the parent repo; the bare repo is created on first push.
	if base, ok := strings.CutSuffix(strings.TrimSuffix(argv[1], ".git"), ".wiki"); ok {
		return runWikiGit(cfg, st, user, scope, service, base, write, stdin, stdout, stderr)
	}

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
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, env, maxPack); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}

// runWikiGit serves a repo's wiki companion. The wiki carries no ref
// policy (no hooks, no merge requests); access is exactly the parent
// repo's, and the bare repo is created on the first push. Deploy keys —
// bound to the repo's own git data for CI — cannot touch the wiki.
func runWikiGit(cfg config.Config, st *store.Store, user store.User, scope, service, basePath string,
	write bool, stdin io.Reader, stdout, stderr io.Writer) int {
	repo, err := st.RepoByPath(basePath)
	if err != nil {
		fmt.Fprintln(stderr, "repository not found")
		return protocol.ExitNotFound
	}
	if policy.IsDeployScope(scope) {
		fmt.Fprintln(stderr, "repository not found")
		return protocol.ExitNotFound
	}
	grant, err := st.AccessRole(repo.ID, user.ID)
	if err != nil {
		fmt.Fprintln(stderr, "internal error")
		return protocol.ExitFailure
	}
	if !policy.CanRead(user, repo, grant) {
		fmt.Fprintln(stderr, "repository not found")
		return protocol.ExitNotFound
	}
	if !policy.ScopeAllowsGit(scope, repo.Path(), write) {
		fmt.Fprintf(stderr, "this key's scope (%s) does not allow %s on the wiki\n", scope, service)
		return protocol.ExitDenied
	}
	if write && !policy.CanWrite(user, repo, grant) {
		fmt.Fprintf(stderr, "write access to %s wiki denied\n", repo.Path())
		return protocol.ExitDenied
	}
	if write && repo.Settings.Archived {
		fmt.Fprintf(stderr, "%s is archived and read-only\n", repo.Path())
		return protocol.ExitDenied
	}
	dir := control.RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name+".wiki")
	if _, err := os.Stat(dir); err != nil {
		if !write {
			fmt.Fprintln(stderr, "this repository has no wiki yet")
			return protocol.ExitNotFound
		}
		if err := gitutil.InitBare(dir, "main", ""); err != nil {
			fmt.Fprintln(stderr, "initializing wiki failed")
			return protocol.ExitFailure
		}
	}
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, nil, cfg.Limits.MaxPackBytes); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}
