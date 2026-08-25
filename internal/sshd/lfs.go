package sshd

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// runLFSAuthenticate answers the git-lfs client's SSH probe:
//
//	git-lfs-authenticate <path> download|upload
//
// with the HTTP endpoint and a short-lived repo- and operation-scoped
// token. Access rules mirror the git transports: download needs read,
// upload needs write; deploy keys authorize by their binding alone, and
// every denial on an invisible repo reads as nonexistence.
func runLFSAuthenticate(cfg config.Config, st *store.Store, user store.User, scope string,
	argv []string, stdout, stderr io.Writer) int {
	if len(argv) != 3 || (argv[2] != "download" && argv[2] != "upload") {
		fmt.Fprintln(stderr, "usage: git-lfs-authenticate <path> download|upload")
		return protocol.ExitUsage
	}
	op := argv[2]
	write := op == "upload"
	repo, err := st.RepoByPath(argv[1])
	if err != nil {
		fmt.Fprintln(stderr, "repository not found")
		return protocol.ExitNotFound
	}
	if policy.IsDeployScope(scope) {
		if !policy.DeployScopeAllows(scope, repo.ID, write) {
			fmt.Fprintln(stderr, "repository not found")
			return protocol.ExitNotFound
		}
	} else {
		grant, err := st.AccessRole(repo.ID, user.ID)
		if err != nil {
			fmt.Fprintln(stderr, "internal error")
			return protocol.ExitFailure
		}
		if !policy.CanRead(user, repo, grant) {
			fmt.Fprintln(stderr, "repository not found")
			return protocol.ExitNotFound
		}
		if !policy.ScopeAllowsGit(scope, repo.Path(), write) {
			fmt.Fprintf(stderr, "this key's scope (%s) does not allow lfs %s on %s\n", scope, op, repo.Path())
			return protocol.ExitDenied
		}
		if write && !policy.CanWrite(user, repo, grant) {
			fmt.Fprintf(stderr, "write access to %s denied\n", repo.Path())
			return protocol.ExitDenied
		}
	}
	if write && repo.Settings.Archived {
		fmt.Fprintf(stderr, "%s is archived and read-only\n", repo.Path())
		return protocol.ExitDenied
	}
	secret, err := st.LFSSecret(lfs.NewSecret)
	if err != nil {
		fmt.Fprintln(stderr, "internal error")
		return protocol.ExitFailure
	}
	token := lfs.Sign([]byte(secret), repo.ID, op, time.Now())
	json.NewEncoder(stdout).Encode(map[string]any{
		"href": fmt.Sprintf("%s/%s/%s.git/info/lfs",
			cfg.Server.SiteURL, repo.OwnerName, repo.Name),
		"header":     map[string]string{"Authorization": "Bearer " + token},
		"expires_in": int(lfs.TokenTTL.Seconds()),
	})
	return protocol.ExitOK
}
