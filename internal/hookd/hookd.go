// Package hookd is the unix-socket bridge between git hooks and the daemon.
// The hook process (forged in hook mode) computes git facts — it inherits
// git's quarantine environment, which the daemon does not see — and sends
// them here; the daemon answers with a pure policy decision.
package hookd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/krazywarez/forge/internal/policy"
	"github.com/krazywarez/forge/internal/store"
)

// Env variable names passed to git transport subprocesses and inherited by
// hooks.
const (
	EnvSocket = "FORGE_HOOK_SOCKET"
	EnvRepoID = "FORGE_REPO_ID"
	EnvUserID = "FORGE_USER_ID"
)

type Request struct {
	Hook    string             `json:"hook"` // pre-receive | post-receive
	RepoID  int64              `json:"repo_id"`
	UserID  int64              `json:"user_id"`
	Updates []policy.RefUpdate `json:"updates"`
}

type Response struct {
	Allow   bool   `json:"allow"`
	Message string `json:"message,omitempty"`
}

// SocketPath returns the hook socket location under the server root.
func SocketPath(root string) string { return filepath.Join(root, "hook.sock") }

type Server struct {
	st *store.Store
}

// Serve listens on the unix socket until the listener is closed.
func Serve(root string, st *store.Store) (func() error, error) {
	path := SocketPath(root)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{st: st}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return ln.Close, nil
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		json.NewEncoder(conn).Encode(Response{Allow: false, Message: "bad hook request"})
		return
	}
	json.NewEncoder(conn).Encode(s.decide(req))
}

func (s *Server) decide(req Request) Response {
	switch req.Hook {
	case "pre-receive":
		repo, err := s.st.RepoByID(req.RepoID)
		if err != nil {
			return Response{Allow: false, Message: "unknown repository"}
		}
		if msg := policy.CheckPush(repo, req.Updates); msg != "" {
			return Response{Allow: false, Message: msg}
		}
		return Response{Allow: true}
	case "post-receive":
		// Event recording and signature verification enqueue land in M4.
		return Response{Allow: true}
	default:
		return Response{Allow: false, Message: fmt.Sprintf("unknown hook %q", req.Hook)}
	}
}

// Ask sends one request from the hook process to the daemon.
func Ask(socketPath string, req Request) (Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// WriteHookScripts (re)generates the shared hooks directory. Called at
// daemon startup so a moved binary self-heals; every repo points here via
// core.hooksPath.
func WriteHookScripts(hooksDir, forgedPath string) error {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	for _, hook := range []string{"pre-receive", "post-receive"} {
		script := fmt.Sprintf("#!/bin/sh\nexec %q hook %s\n", forgedPath, hook)
		if err := os.WriteFile(filepath.Join(hooksDir, hook), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}
