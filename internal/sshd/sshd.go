// Package sshd implements the embedded SSH listener: public-key auth against
// registered keys, then dispatch to git transport or control commands.
package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/krazywarez/forge/internal/config"
	"github.com/krazywarez/forge/internal/control"
	"github.com/krazywarez/forge/internal/protocol"
	"github.com/krazywarez/forge/internal/store"
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
		ServerVersion:     "SSH-2.0-forged",
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
// username is ignored; identity comes from the key alone.
func (s *Server) authenticate(_ ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(pub)
	key, err := s.st.SSHKeyByFingerprint(fp)
	if err != nil {
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
			fmt.Fprintf(ch, "forge control plane: interactive shells are not available.\nTry: ssh %s help\n", s.cfg.Server.SiteURL)
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
	userID, _ := strconv.ParseInt(ext["user-id"], 10, 64)
	keyID, _ := strconv.ParseInt(ext["key-id"], 10, 64)
	user, err := s.st.UserByID(userID)
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "account no longer exists")
		return protocol.ExitDenied
	}
	_ = s.st.TouchSSHKey(keyID)

	if name, _, ok := strings.Cut(cmdline, " "); ok || name != "" {
		switch name {
		case "git-upload-pack", "git-receive-pack", "git-upload-archive":
			fmt.Fprintln(ch.Stderr(), "git transport not implemented (M2)")
			return protocol.ExitFailure
		}
	}

	argv, err := protocol.Tokenize(cmdline)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "cannot parse command: %v\n", err)
		return protocol.ExitUsage
	}
	ctx := &control.Ctx{
		User:   user,
		Scope:  ext["scope"],
		Store:  s.st,
		Cfg:    s.cfg,
		Stdin:  ch,
		Stdout: ch,
		Stderr: ch.Stderr(),
	}
	return control.Dispatch(ctx, argv)
}
