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
	"path"
	"path/filepath"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/ci"
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
		enc.Encode(Response{Allow: false, Message: refusal})
		return
	}
	enc.Encode(Response{Allow: true})
}

// postReceive applies the cross-repo MR effect: a push to a source branch
// refreshes refs/merge-requests/N/head in every target repo, by fetching —
// the target owns the objects, so the MR outlives the fork. This is the only
// place a hook writes outside its own repository.
func (s *Server) postReceive(req Request) {
	pushedRepo, pushedRepoErr := s.st.RepoByID(req.RepoID)
	for _, u := range req.Updates {
		// Every ref update is an event webhooks can subscribe to.
		s.st.RecordEvent(req.RepoID, req.UserID, "push", fmt.Sprintf(
			`{"ref":%q,"old":%q,"new":%q,"forced":%v,"deleted":%v}`,
			u.Ref, u.Old, u.New, u.IsForce, u.IsDelete))

		// Any ref update — branch or tag — schedules the push mirrors.
		s.st.MarkMirrorsDirty(req.RepoID, "push")

		// Tag pushes run the tag-triggered CI jobs.
		if tag, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok && !u.IsDelete && pushedRepoErr == nil {
			s.queueTagBuilds(pushedRepo, req.UserID, tag, u.New)
		}

		branch, ok := cutHeads(u.Ref)
		if !ok {
			continue
		}
		// Commits landing on the default branch act on issue references
		// in their messages (closes #N, plain #N).
		if pushedRepoErr == nil && branch == pushedRepo.DefaultBranch && !u.IsDelete {
			dir := control.RepoDir(s.cfg.Server.Root, pushedRepo.OwnerName, pushedRepo.Name)
			control.ProcessCommitMessages(s.st, dir, pushedRepo, req.UserID, u.Old, u.New)
			control.RecordLandedCommits(s.st, dir, pushedRepo, u.Old, u.New)
		}
		// A branch push with a .gitbay/ci.yml queues one build per job.
		if pushedRepoErr == nil && !u.IsDelete {
			s.queueBuilds(pushedRepo, req.UserID, branch, u.Old, u.New)
		}
		if u.IsForce {
			s.st.Audit(req.UserID, "push.forced", map[string]any{
				"repo": req.RepoID, "ref": u.Ref, "old": u.Old, "new": u.New})
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
			// The merge base as it stands now, so a later range-diff
			// compares each revision against the target it was written
			// on rather than against today's. Best-effort: a base that
			// cannot be worked out costs precision, not the record.
			base, err := gitutil.MergeBase(dstDir, "refs/heads/"+mr.TargetRef, headRef)
			if err != nil {
				base = ""
			}
			if err := s.st.UpdateMRHead(mr.ID, u.New, base); err != nil {
				slog.Error("post-receive: recording MR head", "mr", mr.Number, "err", err)
			}
			if srcRepo.ID != target.ID {
				control.QueueMRBuilds(s.st, s.cfg.Server.Root, s.cfg.Server.SiteURL,
					target, req.UserID, mr.Number, u.New)
			}
			if mr.State == "source_gone" {
				s.st.SetMRState(mr.ID, "open") // branch came back
			}
		}
	}
}

// queueBuilds queues the push jobs for a branch update. The work is
// shared with the merge path, which moves a ref without reaching a hook.
func (s *Server) queueBuilds(repo store.Repo, userID int64, branch, old, sha string) {
	control.QueueBranchBuilds(
		s.st, s.cfg.Server.Root, s.cfg.Server.SiteURL,
		repo, userID, branch, old, sha, time.Now())
}

// queueTagBuilds runs the jobs whose tag pattern matches a pushed tag.
// The build records the tag as its ref and the peeled commit as its sha,
// so statuses land on the commit, not an annotated tag object.
func (s *Server) queueTagBuilds(repo store.Repo, userID int64, tag, pushed string) {
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	sha, err := gitutil.PeelToCommit(dir, pushed)
	if err != nil {
		return
	}
	raw, err := gitutil.ReadBlob(dir, sha, ci.ConfigPath, 1<<16)
	if err != nil {
		return
	}
	jobs, err := ci.Parse(raw)
	if err != nil {
		return // the branch push already reported ci/config
	}
	for _, j := range jobs {
		if j.Tags == "" {
			continue
		}
		if ok, _ := path.Match(j.Tags, tag); !ok {
			continue
		}
		steps, _ := json.Marshal(j.Steps)
		n, err := s.st.CreateBuild(repo.ID, j.Name, sha, tag, string(steps), true)
		if err != nil {
			slog.Error("queueing tag build", "repo", repo.Path(), "job", j.Name, "err", err)
			continue
		}
		url := fmt.Sprintf("%s/%s/builds/%d", s.cfg.Server.SiteURL, repo.Path(), n)
		s.st.SetCommitStatus(repo.ID, sha, "ci/"+j.Name, "pending", "tag "+tag, url, userID)
	}
}

func cutHeads(ref string) (string, bool) {
	const p = "refs/heads/"
	if len(ref) > len(p) && ref[:len(p)] == p {
		return ref[len(p):], true
	}
	return "", false
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
