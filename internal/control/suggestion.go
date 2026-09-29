package control

import (
	"bytes"
	"fmt"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/suggest"
)

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

// readAnchored reads the regular file path at commit, with its mode.
func readAnchored(dir, commit, path string) ([]byte, string, error) {
	e, ok := gitutil.StatPath(dir, commit, path)
	if !ok {
		return nil, "", fmt.Errorf("%s", reasonGone)
	}
	if e.Type != "blob" || (e.Mode != "100644" && e.Mode != "100755") {
		return nil, "", fmt.Errorf("%s is not a regular file", path)
	}
	if e.Size > maxSuggestionBytes {
		return nil, "", fmt.Errorf("%s is larger than %d bytes", path, maxSuggestionBytes)
	}
	content, err := gitutil.ReadBlob(dir, commit, path, maxSuggestionBytes)
	if err != nil {
		return nil, "", err
	}
	return content, e.Mode, nil
}

// suggestionRange is a thread's first and last line.
func suggestionRange(cm store.DiffComment) (int, int) {
	return int(firstNonZero(cm.StartLine, cm.Line)), int(cm.Line)
}

// ThreadSuggestion is the suggestion a thread root carries, checked
// against the merge request's head, or nil when it carries none. The
// original lines are read from the commit the comment was made on, in
// the target repository, which holds every head the merge request has
// had until gc prunes an abandoned one.
func ThreadSuggestion(st *store.Store, root string, repo store.Repo, mr store.MR, cm store.DiffComment) *SuggestionOut {
	if cm.ReplyTo != 0 || cm.Side != "new" {
		return nil
	}
	lines, found, err := suggest.Parse(cm.Body)
	if err != nil || !found {
		return nil
	}
	start, end := suggestionRange(cm)
	s := &SuggestionOut{Path: cm.Path, StartLine: int64(start), EndLine: int64(end), Commit: cm.HeadSHA,
		Replacement: suggest.Text(lines), Apply: "server"}
	if signedOnly(st, repo, mr) {
		s.Apply = "local"
	}
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	if e, ok := gitutil.StatPath(dir, cm.HeadSHA, cm.Path); ok {
		s.Blob = e.SHA
	}
	base, _, err := readAnchored(dir, cm.HeadSHA, cm.Path)
	orig, ok := suggest.Range(base, start, end)
	if err != nil || !ok {
		s.Outdated, s.Reason = true, reasonNoBase
		return s
	}
	s.Original = string(orig)
	s.Reason = anchorReason(dir, mr.HeadSHA, s)
	s.Outdated = s.Reason != ""
	return s
}

// anchorReason says why s cannot be applied to the file at head, or ""
// when the lines it replaces are still what it was made against.
func anchorReason(dir, head string, s *SuggestionOut) string {
	content, _, err := readAnchored(dir, head, s.Path)
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
