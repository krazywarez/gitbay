package control

import (
	"fmt"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// ReadableOrgRepoIDs is the org's repositories user may read. Counts on
// org labels and milestones are taken over these, so a private
// repository's issues never show in a number someone outside it sees. A
// zero user is anonymous.
func ReadableOrgRepoIDs(st *store.Store, user store.User, orgID int64) ([]int64, error) {
	repos, err := st.ListReposForOwner("org", orgID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range repos {
		grant := ""
		if user.ID != 0 {
			if grant, err = st.AccessRole(r.ID, user.ID); err != nil {
				return nil, err
			}
		}
		if policy.CanRead(user, r, grant) {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// ReadableScope is the set a repository's label and milestone counts
// span: its org's readable repositories, or just itself when a user owns
// it. The caller has already been allowed to read repo.
func ReadableScope(st *store.Store, user store.User, repo store.Repo) ([]int64, error) {
	if repo.OwnerKind == "org" {
		return ReadableOrgRepoIDs(st, user, repo.OwnerID)
	}
	return []int64{repo.ID}, nil
}

// orgScopedMsg names the org command that manages a row a repository
// command was asked to change.
func orgScopedMsg(repo store.Repo, noun, name, verb string) string {
	return fmt.Sprintf("%s is an org %s of %s; manage it with org %s %s %s %s", name, noun, repo.OwnerName, noun, verb, repo.OwnerName, name)
}
