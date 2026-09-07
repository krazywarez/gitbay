package policy

import (
	"path"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// CanRead reports whether user may read repo over an authenticated channel.
// Public repos are readable by any authenticated user; private repos require
// ownership or an explicit grant.
func CanRead(user store.User, repo store.Repo, grant string) bool {
	if isOwner(user, repo) {
		return true
	}
	if repo.Visibility == "public" {
		return true
	}
	return grant == "read" || grant == "write" || grant == "admin"
}

// CanWrite reports whether user may push to repo.
func CanWrite(user store.User, repo store.Repo, grant string) bool {
	if isOwner(user, repo) {
		return true
	}
	return grant == "write" || grant == "admin"
}

// CanAdmin reports whether user may change repo settings and access.
func CanAdmin(user store.User, repo store.Repo, grant string) bool {
	if isOwner(user, repo) {
		return true
	}
	return grant == "admin"
}

func isOwner(user store.User, repo store.Repo) bool {
	return repo.OwnerKind == "user" && repo.OwnerID == user.ID
}

// ScopeAllowsGit reports whether an account-scoped SSH key permits git
// transport at all. Deploy scopes are decided by DeployScopeAllows instead.
func ScopeAllowsGit(scope, repoPath string, write bool) bool {
	switch scope {
	case "full", "git":
		return true
	case "runner":
		// A CI runner clones what it builds and pushes nothing.
		return !write
	}
	return false
}

// DeployScopeAllows authorizes a deploy key purely by its scope: the key is
// bound to a repository ID (rename- and transfer-proof), grants nothing
// anywhere else, and never inherits the access of whoever registered it.
func DeployScopeAllows(scope string, repoID int64, write bool) bool {
	rest, ok := strings.CutPrefix(scope, "deploy:")
	if !ok {
		return false
	}
	idStr, mode, ok := strings.Cut(rest, ":")
	if !ok || idStr != strconv.FormatInt(repoID, 10) {
		return false
	}
	switch mode {
	case "rw":
		return true
	case "ro":
		return !write
	}
	return false
}

// IsDeployScope reports whether a key scope is a deploy binding.
func IsDeployScope(scope string) bool { return strings.HasPrefix(scope, "deploy:") }

// RefUpdate is one proposed ref change, with git facts computed by the hook
// process (which can see quarantined objects; the daemon cannot).
type RefUpdate struct {
	Ref      string `json:"ref"`
	Old      string `json:"old"`
	New      string `json:"new"`
	IsDelete bool   `json:"is_delete"`
	IsForce  bool   `json:"is_force"`
}

// CheckPush applies ref policy for a push by a user with write access
// already established. It returns a denial message, or "" to allow.
func CheckPush(repo store.Repo, updates []RefUpdate) string {
	protected := map[string]bool{}
	for _, b := range repo.Settings.ProtectedBranches {
		protected["refs/heads/"+b] = true
	}
	for _, u := range updates {
		if strings.HasPrefix(u.Ref, "refs/merge-requests/") {
			return "refs/merge-requests/* is server-owned and cannot be pushed"
		}
		// A protected tag is created once. Its globs match the tag name.
		if tag, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok && TagProtected(repo, tag) {
			if u.IsDelete {
				return "tag " + tag + " is protected: deletion refused"
			}
			if !isZeroSHA(u.Old) {
				return "tag " + tag + " is protected: update refused"
			}
		}
		if protected[u.Ref] {
			branch := strings.TrimPrefix(u.Ref, "refs/heads/")
			if u.IsDelete {
				return "branch " + branch + " is protected: deletion refused"
			}
			if u.IsForce {
				return "branch " + branch + " is protected: force-push refused"
			}
			// Under require_mr the server's merge is the only writer of an
			// existing protected branch. Creating one is still a push:
			// there is nothing to route a merge request into yet.
			if repo.Settings.RequireMR && !isZeroSHA(u.Old) {
				return "branch " + branch + " accepts changes through merge requests only"
			}
		}
	}
	return ""
}

// TagProtected reports whether a tag name matches one of the repository's
// protected-tag globs.
func TagProtected(repo store.Repo, tag string) bool {
	for _, g := range repo.Settings.ProtectedTags {
		if ok, _ := path.Match(g, tag); ok {
			return true
		}
	}
	return false
}

func isZeroSHA(sha string) bool { return sha != "" && strings.Trim(sha, "0") == "" }
