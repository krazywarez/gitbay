package policy

import (
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

// ScopeAllowsGit reports whether an SSH key scope permits the requested git
// transport on repoPath ("owner/name"). write=true for receive-pack.
func ScopeAllowsGit(scope, repoPath string, write bool) bool {
	switch scope {
	case "full", "git":
		return true
	}
	rest, ok := strings.CutPrefix(scope, "deploy:")
	if !ok {
		return false
	}
	target, mode, ok := strings.Cut(rest, ":")
	if !ok || target != repoPath {
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
		if protected[u.Ref] {
			branch := strings.TrimPrefix(u.Ref, "refs/heads/")
			if u.IsDelete {
				return "branch " + branch + " is protected: deletion refused"
			}
			if u.IsForce {
				return "branch " + branch + " is protected: force-push refused"
			}
		}
	}
	return ""
}
