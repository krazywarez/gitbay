// Package hookd is the unix-socket bridge between git hooks and the daemon.
// The hook process (gitbayd in hook mode) computes git facts — it inherits
// git's quarantine environment, which the daemon does not see — and sends
// them here; the daemon answers with a policy decision.
//
// pre-receive is two-phase when the repo requires signed commits: the first
// response sets NeedCommits, and the hook answers with the raw commit
// objects (only the hook can read them out of quarantine) for verification.
package hookd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/sig"
	"gitbay.org/gitbay/internal/store"
)

// Env variable names passed to git transport subprocesses and inherited by
// hooks.
const (
	EnvSocket = "GITBAY_HOOK_SOCKET"
	EnvRepoID = "GITBAY_REPO_ID"
	EnvUserID = "GITBAY_USER_ID"
)

type Request struct {
	Hook    string             `json:"hook"` // pre-receive | post-receive
	RepoID  int64              `json:"repo_id"`
	UserID  int64              `json:"user_id"`
	Updates []policy.RefUpdate `json:"updates"`
}

type RawCommit struct {
	SHA string `json:"sha"`
	Raw []byte `json:"raw"`
}

// CommitsPayload is the hook's second message when NeedCommits was set.
type CommitsPayload struct {
	Commits []RawCommit `json:"commits"`
}

type Response struct {
	Allow       bool   `json:"allow"`
	Message     string `json:"message,omitempty"`
	NeedCommits bool   `json:"need_commits,omitempty"`
}

// SocketPath returns the hook socket location. It prefers the server root,
// but unix socket paths are capped (~104 bytes on macOS, 108 on Linux), so
// deep roots fall back to a hashed name under the system temp directory.
// Hooks receive the chosen path via GITBAY_HOOK_SOCKET, so both sides always
// agree.
func SocketPath(root string) string {
	p := filepath.Join(root, "hook.sock")
	if len(p) <= 100 {
		return p
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(os.TempDir(), fmt.Sprintf("gitbay-%x.sock", sum[:8]))
}

type Server struct {
	cfg config.Config
	st  *store.Store
}

// Serve listens on the unix socket until the listener is closed.
func Serve(cfg config.Config, st *store.Store) (func() error, error) {
	path := SocketPath(cfg.Server.Root)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, st: st}
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
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var req Request
	if err := dec.Decode(&req); err != nil {
		enc.Encode(Response{Allow: false, Message: "bad hook request"})
		return
	}
	switch req.Hook {
	case "pre-receive":
		s.preReceive(req, dec, enc)
	case "post-receive":
		s.postReceive(req)
		enc.Encode(Response{Allow: true})
	default:
		enc.Encode(Response{Allow: false, Message: fmt.Sprintf("unknown hook %q", req.Hook)})
	}
}

func (s *Server) preReceive(req Request, dec *json.Decoder, enc *json.Encoder) {
	repo, err := s.st.RepoByID(req.RepoID)
	if err != nil {
		enc.Encode(Response{Allow: false, Message: "unknown repository"})
		return
	}
	if msg := policy.CheckPush(repo, req.Updates); msg != "" {
		enc.Encode(Response{Allow: false, Message: msg})
		return
	}
	if !repo.Settings.RequireSignedCommits {
		enc.Encode(Response{Allow: true})
		return
	}

	// Phase two: ask the hook for the incoming commit objects.
	if err := enc.Encode(Response{Allow: true, NeedCommits: true}); err != nil {
		return
	}
	var payload CommitsPayload
	if err := dec.Decode(&payload); err != nil {
		enc.Encode(Response{Allow: false, Message: "bad commits payload"})
		return
	}
	db := store.SigDB{Store: s.st}
	for _, rc := range payload.Commits {
		parsed, err := sig.ParseCommit(rc.Raw)
		if err != nil {
			enc.Encode(Response{Allow: false, Message: fmt.Sprintf("unparseable commit %s", rc.SHA)})
			return
		}
		res, err := sig.VerifyCommit(db, parsed)
		if err != nil || res.State != sig.Verified {
			state := "error"
			if err == nil {
				state = string(res.State)
			}
			enc.Encode(Response{Allow: false, Message: fmt.Sprintf(
				"this repository requires signed commits: %.10s is %s", rc.SHA, state)})
			return
		}
	}
	enc.Encode(Response{Allow: true})
}

// postReceive applies the cross-repo MR effect: a push to a source branch
// refreshes refs/merge-requests/N/head in every target repo, by fetching —
// the target owns the objects, so the MR outlives the fork. This is the only
// place a hook writes outside its own repository.
func (s *Server) postReceive(req Request) {
	for _, u := range req.Updates {
		branch, ok := cutHeads(u.Ref)
		if !ok {
			continue
		}
		mrs, err := s.st.OpenMRsBySource(req.RepoID, branch)
		if err != nil {
			slog.Error("post-receive: listing MRs", "err", err)
			continue
		}
		srcRepo, err := s.st.RepoByID(req.RepoID)
		if err != nil {
			continue
		}
		srcDir := control.RepoDir(s.cfg.Server.Root, srcRepo.OwnerName, srcRepo.Name)
		for _, mr := range mrs {
			target, err := s.st.RepoByID(mr.RepoID)
			if err != nil {
				continue
			}
			if u.IsDelete {
				if mr.State == "open" {
					s.st.SetMRState(mr.ID, "source_gone")
				}
				continue // head ref retained: the diff stays viewable
			}
			dstDir := control.RepoDir(s.cfg.Server.Root, target.OwnerName, target.Name)
			headRef := fmt.Sprintf("refs/merge-requests/%d/head", mr.Number)
			if err := gitutil.FetchInto(dstDir, srcDir, u.New, headRef); err != nil {
				slog.Error("post-receive: refreshing MR head", "mr", mr.Number, "err", err)
				continue
			}
			if err := s.st.UpdateMRHead(mr.ID, u.New); err != nil {
				slog.Error("post-receive: recording MR head", "mr", mr.Number, "err", err)
			}
			if mr.State == "source_gone" {
				s.st.SetMRState(mr.ID, "open") // branch came back
			}
		}
	}
}

func cutHeads(ref string) (string, bool) {
	const p = "refs/heads/"
	if len(ref) > len(p) && ref[:len(p)] == p {
		return ref[len(p):], true
	}
	return "", false
}

// Ask sends one request from the hook process to the daemon. commits is
// called if the daemon asks for the incoming commit objects.
func Ask(socketPath string, req Request, commits func() (CommitsPayload, error)) (Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	if !resp.NeedCommits {
		return resp, nil
	}
	payload, err := commits()
	if err != nil {
		return Response{}, err
	}
	if err := enc.Encode(payload); err != nil {
		return Response{}, err
	}
	err = dec.Decode(&resp)
	return resp, err
}

// WriteHookScripts (re)generates the shared hooks directory. Called at
// daemon startup so a moved binary self-heals; every repo points here via
// core.hooksPath.
func WriteHookScripts(hooksDir, gitbaydPath string) error {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	for _, hook := range []string{"pre-receive", "post-receive"} {
		script := fmt.Sprintf("#!/bin/sh\nexec %q hook %s\n", gitbaydPath, hook)
		if err := os.WriteFile(filepath.Join(hooksDir, hook), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}
