// Package sshd implements the embedded SSH listener: public-key auth against
// registered keys, then dispatch to git transport or control commands.
package sshd

import (
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
	cfg   config.Config
	st    *store.Store
	sshCfg *ssh.ServerConfig
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	s := &Server{cfg: cfg, st: st}

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
func (s *Server) authenticate(_ ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(pub)
	key, err := s.st.SSHKeyByFingerprint(fp)
	if err != nil {
		if s.cfg.Registration.Mode != "closed" {
			return &ssh.Permissions{Extensions: map[string]string{
				"anon-key": base64.StdEncoding.EncodeToString(pub.Marshal()),
			}}, nil
		}
		return nil, fmt.Errorf("unknown key %s", fp)
	}
	return &ssh.Permissions{Extensions: map[string]string{
		"user-id": strconv.FormatInt(key.UserID, 10),
		"key-id":  strconv.FormatInt(key.ID, 10),
		"scope":   key.Scope,
	}}, nil
}

// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.sshCfg)
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
		go s.handleSession(sconn, ch, chReqs)
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
	return Exec(s.cfg, s.st, user, ext["scope"], cmdline, ch, ch, ch.Stderr())
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
func Exec(cfg config.Config, st *store.Store, user store.User, scope, cmdline string,
	stdin io.Reader, stdout, stderr io.Writer) int {
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
		}
	}
	ctx := &control.Ctx{
		User:   user,
		Scope:  scope,
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

	dir := control.RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	env := []string{
		hookd.EnvSocket + "=" + hookd.SocketPath(cfg.Server.Root),
		hookd.EnvRepoID + "=" + strconv.FormatInt(repo.ID, 10),
		hookd.EnvUserID + "=" + strconv.FormatInt(user.ID, 10),
	}
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, env); err != nil {
		return protocol.ExitFailure
	}
	return protocol.ExitOK
}
