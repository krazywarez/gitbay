package control

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/suggest"
)

func init() {
	register(Command{Path: []string{"mr", "apply-suggestion"},
		Summary:  "commit a review thread's suggestion to the source branch",
		Usage:    "mr apply-suggestion <owner/name> <n> <thread-id>",
		Examples: []string{"mr apply-suggestion krz/gitbay 431 12"},
		Run:      runMRApplySuggestion})
}

// SuggestionOut is the change a review thread's ```suggestion block
// proposes: lines StartLine through EndLine of Path, as they were at
// Commit (Original), replaced by Replacement. Both texts end every line
// with its terminator, so "" is no lines. Apply is "server" where `mr
// apply-suggestion` commits it, or "local" on a repository requiring
// signed commits, where the CLI commits it with the user's own key.
type SuggestionOut struct {
	Path        string `json:"path"`
	StartLine   int64  `json:"start_line"`
	EndLine     int64  `json:"end_line"`
	Commit      string `json:"commit"`
	Blob        string `json:"blob,omitempty"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	Outdated    bool   `json:"outdated"`
	Reason      string `json:"reason,omitempty"`
	Apply       string `json:"apply"`
}

// maxSuggestionBytes bounds the file a suggestion rewrites, the same as
// a single-file commit over the control plane.
const maxSuggestionBytes = maxCommitFileBytes

// Reasons a suggestion cannot be applied at a head.
const (
	reasonGone    = "the file is not at the head: it was renamed or deleted"
	reasonChanged = "the lines it replaces have changed since it was made"
	reasonNoBase  = "the commit it was made against is no longer available"
)

// anchoredFiles reads the files suggestions anchor in, for one page or
// listing: one ls-tree per (commit, path), and every blob through one
// cat-file --batch process started on first use, so a merge request with
// many suggestions on a file costs no more processes than one with a
// single suggestion.
type anchoredFiles struct {
	dir     string
	entries map[[2]string]anchoredEntry
	blobs   map[string][]byte
	batch   *gitutil.BlobBatch
	spawned int // git processes started, for the test that bounds it
}

type anchoredEntry struct {
	e  gitutil.TreeEntry
	ok bool
}

func newAnchoredFiles(dir string) *anchoredFiles {
	return &anchoredFiles{dir: dir, entries: map[[2]string]anchoredEntry{}, blobs: map[string][]byte{}}
}

func (f *anchoredFiles) close() {
	if f.batch != nil {
		f.batch.Close()
	}
}

// read returns the regular file path at commit, with its mode and blob id.
func (f *anchoredFiles) read(commit, path string) ([]byte, string, string, error) {
	key := [2]string{commit, path}
	ent, seen := f.entries[key]
	if !seen {
		f.spawned++
		ent.e, ent.ok = gitutil.StatPath(f.dir, commit, path)
		f.entries[key] = ent
	}
	e := ent.e
	if !ent.ok {
		return nil, "", "", fmt.Errorf("%s", reasonGone)
	}
	if e.Type != "blob" || (e.Mode != "100644" && e.Mode != "100755") {
		return nil, "", "", fmt.Errorf("%s is not a regular file", path)
	}
	if e.Size > maxSuggestionBytes {
		return nil, "", "", fmt.Errorf("%s is larger than %d bytes", path, maxSuggestionBytes)
	}
	if content, ok := f.blobs[e.SHA]; ok {
		return content, e.Mode, e.SHA, nil
	}
	if f.batch == nil {
		b, err := gitutil.NewBlobBatch(f.dir)
		if err != nil {
			return nil, "", "", err
		}
		f.spawned++
		f.batch = b
	}
	content, err := f.batch.Read(e.SHA, maxSuggestionBytes)
	if err != nil {
		return nil, "", "", err
	}
	f.blobs[e.SHA] = content
	return content, e.Mode, e.SHA, nil
}

// readAnchored reads the regular file path at commit, with its mode.
func readAnchored(dir, commit, path string) ([]byte, string, error) {
	f := newAnchoredFiles(dir)
	defer f.close()
	content, mode, _, err := f.read(commit, path)
	return content, mode, err
}

// suggestionRange is a thread's first and last line.
func suggestionRange(cm store.DiffComment) (int, int) {
	return int(firstNonZero(cm.StartLine, cm.Line)), int(cm.Line)
}

// Suggestions are the suggestions the thread roots among comments carry,
// by thread id, each checked against the merge request's head. The
// original lines are read from the commit the comment was made on, in
// the target repository, which holds every head the merge request has
// had until gc prunes an abandoned one.
func Suggestions(st *store.Store, root string, repo store.Repo, mr store.MR, comments []store.DiffComment) map[int64]*SuggestionOut {
	f := newAnchoredFiles(RepoDir(root, repo.OwnerName, repo.Name))
	defer f.close()
	return suggestionsWith(f, func() bool { return signedOnly(st, repo, mr) }, mr, comments)
}

// suggestionsWith is Suggestions reading through f. signed is asked
// once, and only when there is a suggestion.
func suggestionsWith(f *anchoredFiles, signed func() bool, mr store.MR, comments []store.DiffComment) map[int64]*SuggestionOut {
	out := map[int64]*SuggestionOut{}
	apply := ""
	for _, cm := range comments {
		if cm.ReplyTo != 0 || cm.Side != "new" {
			continue
		}
		lines, found, err := suggest.Parse(cm.Body)
		if err != nil || !found {
			continue
		}
		if apply == "" {
			apply = "server"
			if signed() {
				apply = "local"
			}
		}
		start, end := suggestionRange(cm)
		s := &SuggestionOut{Path: cm.Path, StartLine: int64(start), EndLine: int64(end), Commit: cm.HeadSHA,
			Replacement: suggest.Text(lines), Apply: apply}
		out[cm.ID] = s
		base, _, blob, err := f.read(cm.HeadSHA, cm.Path)
		s.Blob = blob
		orig, ok := suggest.Range(base, start, end)
		if err != nil || !ok {
			s.Outdated, s.Reason = true, reasonNoBase
			continue
		}
		s.Original = string(orig)
		s.Reason = anchorReason(f, mr.HeadSHA, s)
		s.Outdated = s.Reason != ""
	}
	return out
}

// anchorReason says why s cannot be applied to the file at head, or ""
// when the lines it replaces are still what it was made against.
func anchorReason(f *anchoredFiles, head string, s *SuggestionOut) string {
	content, _, _, err := f.read(head, s.Path)
	if err != nil {
		return err.Error()
	}
	now, ok := suggest.Range(content, int(s.StartLine), int(s.EndLine))
	if !ok || !bytes.Equal(now, []byte(s.Original)) {
		return reasonChanged
	}
	return ""
}

// signedOnly reports whether the server may not commit a suggestion for
// this merge request: the source branch or the target it merges into
// requires signed commits, and the server has no key to sign with.
func signedOnly(st *store.Store, repo store.Repo, mr store.MR) bool {
	if repo.Settings.RequireSignedCommits {
		return true
	}
	if mr.SourceRepoID == repo.ID {
		return false
	}
	src, err := st.RepoByID(mr.SourceRepoID)
	return err != nil || src.Settings.RequireSignedCommits
}

// runMRApplySuggestion commits a thread's suggestion to the merge
// request's source branch as the caller, and resolves the thread.
//
// It is a push by the caller, and is held to what a push is: the caller
// needs write on the source repository (for a fork, the fork's writers;
// write on the target grants nothing there), the update goes through the
// pre-receive ref policy (policy.CheckPush: protected branches,
// require-mr) before a compare-and-swap ref update, and RefsUpdated then
// does what post-receive does, which moves the merge request's head and
// reaches a queued merge. The server has no signing key, so where either
// repository requires signed commits it refuses, and the CLI applies the
// suggestion locally instead.
func runMRApplySuggestion(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.usage()
	}
	repo, mr, code := mrRef(c, args[:2], policy.CanRead)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	threadID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad thread id %q", args[2])
	}
	cm, err := c.Store.DiffCommentByID(mr.ID, threadID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cm.Pending && cm.Author != c.User.Username) {
		return c.fail(protocol.ExitNotFound, "no thread %d on %s!%d", threadID, repo.Path(), mr.Number)
	}
	if err != nil {
		return c.failErr(err)
	}
	if cm.ReplyTo != 0 {
		return c.fail(protocol.ExitUsage, "%d is a reply; name the thread root %d", threadID, cm.ReplyTo)
	}
	if cm.Pending {
		return c.fail(protocol.ExitUsage, "thread %d is in your unsubmitted review; submit it with `mr review` first", threadID)
	}
	if mr.State != "open" {
		return c.fail(protocol.ExitUsage, "%s!%d is %s", repo.Path(), mr.Number, mr.State)
	}
	s := Suggestions(c.Store, c.Cfg.Server.Root, repo, mr, []store.DiffComment{cm})[cm.ID]
	if s == nil {
		return c.fail(protocol.ExitUsage, "thread %d carries no suggestion", threadID)
	}

	src, err := c.Store.RepoByID(mr.SourceRepoID)
	if err != nil {
		return c.failErr(err)
	}
	grant, err := c.Store.AccessRole(src.ID, c.User.ID)
	if err != nil {
		return c.failErr(err)
	}
	source := mr.SourceRef
	if src.ID != repo.ID {
		source = src.Path() + ":" + mr.SourceRef
	}
	if !policy.CanWrite(c.User, src, grant) {
		return c.fail(protocol.ExitDenied, "applying a suggestion pushes to %s; only its writers can", source)
	}
	if src.Settings.Archived {
		return c.fail(protocol.ExitDenied, "%s is archived and read-only", src.Path())
	}
	if mirrored, err := c.Store.PullMirrored(src.ID); err != nil {
		return c.failErr(err)
	} else if mirrored {
		return c.fail(protocol.ExitDenied, "%s is a pull mirror: its refs come from the upstream", src.Path())
	}
	if s.Apply == "local" {
		return c.fail(protocol.ExitDenied,
			"%s requires signed commits and the server cannot sign one; apply it from a clone, which commits with your own key: gitbay mr apply-suggestion %s %d %d",
			source, repo.Path(), mr.Number, threadID)
	}
	if code := checkStorageQuota(c, src); code >= 0 {
		return code
	}
	email, err := c.Store.PrimaryVerifiedEmail(c.User.ID)
	if err != nil {
		return c.failErr(err)
	}
	if email == "" {
		return c.fail(protocol.ExitDenied, "commits carry your identity: your account needs a verified primary email")
	}

	srcDir := RepoDir(c.Cfg.Server.Root, src.OwnerName, src.Name)
	ref := "refs/heads/" + mr.SourceRef
	tip, err := gitutil.ResolveRef(srcDir, ref)
	if err != nil {
		return c.fail(protocol.ExitUsage, "the source branch %s is gone", source)
	}
	if s.Reason == reasonNoBase {
		return c.fail(protocol.ExitUsage, "suggestion in thread %d cannot be applied: %s", threadID, s.Reason)
	}
	files := newAnchoredFiles(srcDir)
	defer files.close()
	if reason := anchorReason(files, tip, s); reason != "" {
		return c.fail(protocol.ExitUsage, "suggestion in thread %d is outdated: %s", threadID, reason)
	}
	content, mode, _, err := files.read(tip, s.Path)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	updated, err := suggest.Apply(content, int(s.StartLine), int(s.EndLine), suggest.FromText(s.Replacement))
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if bytes.Equal(updated, content) {
		return c.fail(protocol.ExitUsage, "the suggestion in thread %d changes nothing", threadID)
	}
	message := suggest.Message(repo.Path(), mr.Number, threadID, cm.Author)
	sha, err := gitutil.CommitWithFile(srcDir, tip, s.Path, mode, updated, c.User.Username, email, message)
	if err != nil {
		return c.failErr(err)
	}
	updates := []policy.RefUpdate{{Ref: ref, Old: tip, New: sha}}
	if msg := policy.CheckPush(src, updates); msg != "" {
		return c.fail(protocol.ExitDenied, "%s", msg)
	}
	if err := gitutil.UpdateRefCAS(srcDir, ref, sha, tip); err != nil {
		return c.fail(protocol.ExitFailure, "the source branch moved; reload and retry")
	}
	RefsUpdated(c.Store, c.Cfg, src.ID, c.User.ID, c.Scope, updates)
	if err := c.Store.SetThreadResolved(mr.ID, threadID, c.User.ID, true); err != nil {
		return c.failErr(err)
	}
	TryQueuedMerge(c.Store, c.Cfg, mr.ID)
	return c.emit(map[string]any{"thread": threadID, "sha": sha, "source": source, "resolved": true}, func(w io.Writer) {
		fmt.Fprintf(w, "applied thread %d to %s at %.10s; thread resolved\n", threadID, source, sha)
	})
}
