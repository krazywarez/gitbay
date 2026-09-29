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
	"strings"

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
	EnvScope  = "GITBAY_KEY_SCOPE"
	// EnvToken names the receive-pack this hook runs under. sshd mints
	// it per push; hookd answers only a request carrying a live one
	// whose repository, account and scope match the request's.
	EnvToken = "GITBAY_PUSH_TOKEN"
)

type Request struct {
	Hook   string `json:"hook"` // pre-receive | post-receive
	RepoID int64  `json:"repo_id"`
	UserID int64  `json:"user_id"`
	// Scope is the pushing key's scope. The user id alone is the account
	// the key belongs to, and a deploy key grants nothing outside its
	// binding, so anything acting on another repository needs this too.
	Scope   string             `json:"scope"`
	Token   string             `json:"token"`
	Updates []policy.RefUpdate `json:"updates"`
}

// RawCommit is one incoming commit object. When NeedCommits is set the
// hook streams these one per JSON value and ends with a zero one, rather
// than sending a single message holding every commit in the push: an
// initial push of a large history is tens of thousands of them (#100).
type RawCommit struct {
	SHA string `json:"sha"`
	Raw []byte `json:"raw"`
}

// Done marks the end of the commit stream.
func (c RawCommit) Done() bool { return c.SHA == "" }

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
	// Listen creates the socket under the process umask. Hooks run as
	// the daemon's own user; nobody else has a reason to connect.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
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
	if err := peerCheck(conn); err != nil {
		slog.Warn("hook socket: refused connection", "err", err)
		// Nothing about the request is known yet, and no account.
		control.AuditRefused(s.st, 0, "refused hook", map[string]any{"reason": err.Error()})
		enc.Encode(Response{Allow: false, Message: "hook socket: " + err.Error()})
		return
	}
	var req Request
	if err := dec.Decode(&req); err != nil {
		enc.Encode(Response{Allow: false, Message: "bad hook request"})
		return
	}
	if actor, msg := s.authorize(req); msg != "" {
		control.AuditRefused(s.st, actor, "refused hook",
			map[string]any{"repo_id": req.RepoID, "hook": req.Hook, "reason": msg})
		enc.Encode(Response{Allow: false, Message: msg})
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

// authorize ties a request to a receive-pack sshd started: its token
// must be live and name the same repository, account and key scope.
// On a refusal actor is the token's account when the token is live,
// and 0 otherwise: the request's own user id is only a claim.
func (s *Server) authorize(req Request) (actor int64, msg string) {
	if req.Token == "" {
		return 0, "push not started by this server"
	}
	tok, err := s.st.PushTokenByHash(store.HashToken(req.Token))
	if err != nil {
		return 0, "push not started by this server"
	}
	if tok.RepoID != req.RepoID || tok.UserID != req.UserID || tok.Scope != req.Scope {
		return tok.UserID, "push token does not match this request"
	}
	return 0, ""
}

// peerCheck is checkPeer; tests replace it.
var peerCheck = checkPeer

// auditedRefs is how many ref names a refused-push row keeps; the rest
// are counted, so one push of many refs cannot write an unbounded row.
const auditedRefs = 20

// refusePush answers a pre-receive refusal and audits it.
func (s *Server) refusePush(enc *json.Encoder, req Request, repo store.Repo, msg string) {
	n := min(len(req.Updates), auditedRefs)
	refs := make([]string, n)
	for i, u := range req.Updates[:n] {
		refs[i] = u.Ref
	}
	data := map[string]any{"repo": repo.Path(), "refs": refs, "reason": msg}
	if more := len(req.Updates) - n; more > 0 {
		data["more_refs"] = more
	}
	control.AuditRefused(s.st, req.UserID, "refused push", data)
	enc.Encode(Response{Allow: false, Message: msg})
}

func (s *Server) preReceive(req Request, dec *json.Decoder, enc *json.Encoder) {
	repo, err := s.st.RepoByID(req.RepoID)
	if err != nil {
		enc.Encode(Response{Allow: false, Message: "unknown repository"})
		return
	}
	if msg := policy.CheckPush(repo, req.Updates); msg != "" {
		s.refusePush(enc, req, repo, msg)
		return
	}
	if msg := s.releaseAnchors(repo, req.Updates); msg != "" {
		s.refusePush(enc, req, repo, msg)
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
	// Each commit is verified as it arrives, so nothing holds the push in
	// memory. The first refusal decides the answer, but the stream is
	// still drained to its end before replying: the hook is writing, and
	// answering early would leave it writing into a socket nobody reads.
	// Draining costs a decode per commit and no verification.
	db := store.SigDB{Store: s.st}
	refusal := ""
	for {
		var rc RawCommit
		if err := dec.Decode(&rc); err != nil {
			enc.Encode(Response{Allow: false, Message: "bad commits payload"})
			return
		}
		if rc.Done() {
			break
		}
		if refusal != "" {
			continue
		}
		parsed, err := sig.ParseCommit(rc.Raw)
		if err != nil {
			refusal = fmt.Sprintf("unparseable commit %s", rc.SHA)
			continue
		}
		res, err := sig.VerifyCommit(db, parsed)
		if err != nil || res.State != sig.Verified {
			state := "error"
			if err == nil {
				state = string(res.State)
			}
			refusal = fmt.Sprintf("this repository requires signed commits: %.10s is %s", rc.SHA, state)
		}
	}
	if refusal != "" {
		s.refusePush(enc, req, repo, refusal)
		return
	}
	enc.Encode(Response{Allow: true})
}

// releaseAnchors refuses deleting or moving a tag that a release is
// anchored to. A release outliving its tag served assets for a commit
// nobody could reach (#201); the release goes first, then the tag.
func (s *Server) releaseAnchors(repo store.Repo, updates []policy.RefUpdate) string {
	for _, u := range updates {
		tag, ok := strings.CutPrefix(u.Ref, "refs/tags/")
		if !ok || gitutil.ZeroSHA(u.Old) {
			continue
		}
		if _, err := s.st.ReleaseByTag(repo.ID, tag); err != nil {
			continue
		}
		verb := "moved"
		if u.IsDelete {
			verb = "deleted"
		}
		return fmt.Sprintf("tag %s anchors a release and cannot be %s: delete the release first", tag, verb)
	}
	return ""
}

// postReceive runs the ref-update work for a push; see
// control.RefsUpdated, which server-side writes to a branch share.
func (s *Server) postReceive(req Request) {
	control.RefsUpdated(s.st, s.cfg, req.RepoID, req.UserID, req.Scope, req.Updates)
}

// Ask sends one request from the hook process to the daemon. stream is
// called if the daemon asks for the incoming commit objects; it hands each
// commit to the callback, which writes it on the wire.
func Ask(socketPath string, req Request, stream func(emit func(RawCommit) error) error) (Response, error) {
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
	if err := stream(func(rc RawCommit) error { return enc.Encode(rc) }); err != nil {
		return Response{}, err
	}
	if err := enc.Encode(RawCommit{}); err != nil { // end of stream
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
