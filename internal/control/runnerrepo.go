package control

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Runners attached to a repository (#184). A runner key claims builds only
// for the repositories it is attached to; a repository admin attaches it
// by pasting the runner's public key. The key lands on the admin's own
// account with scope runner, which confines it to the runner protocol and
// read-only git.
func init() {
	register(Command{Path: []string{"repo", "runner", "add"},
		Summary:    "attach a runner's public key to a repository",
		Usage:      "repo runner add <owner/name> < key.pub",
		ReadsStdin: true, Run: runRepoRunnerAdd})
	register(Command{Path: []string{"repo", "runner", "list"},
		Summary: "list the runners attached to a repository",
		Usage:   "repo runner list <owner/name>", ReadOnly: true, Run: runRepoRunnerList})
	register(Command{Path: []string{"repo", "runner", "remove"},
		Summary: "detach a runner from a repository",
		Usage:   "repo runner remove <owner/name> <fingerprint>", Run: runRepoRunnerRemove})
}

func runRepoRunnerAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{MaxPos: 1, Usage: "repo runner add <owner/name> < key.pub"})
	if err != nil || len(f.Pos) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo runner add <owner/name> < key.pub")
	}
	repo, code := resolveRepo(c, f.Pos[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "not a valid public key in authorized_keys format: %v", err)
	}
	fp := ssh.FingerprintSHA256(pub)
	key, err := c.Store.SSHKeyByFingerprint(fp)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if err := c.Store.AddSSHKey(c.User.ID, fp, pub.Type(), pub.Marshal(), "runner"); err != nil {
			return c.fail(protocol.ExitFailure, "adding key: %v", err)
		}
		if key, err = c.Store.SSHKeyByFingerprint(fp); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	case err != nil:
		return c.fail(protocol.ExitFailure, "%v", err)
	case key.Scope != "runner":
		// A full key would let a build step administer the account; a
		// deploy key is bound elsewhere. A runner gets a key of its own.
		return c.fail(protocol.ExitDenied, "%s is a %s key, not a runner key; give the runner a key of its own", fp, key.Scope)
	case key.UserID != c.User.ID && !c.User.IsAdmin:
		return c.fail(protocol.ExitDenied, "%s belongs to another account", fp)
	}
	// The runner clones what it builds, so the key's account must be able
	// to read the repository. The caller's own key needs no check: they
	// hold admin on the repository to get here.
	if key.UserID != c.User.ID {
		owner, err := c.Store.UserByID(key.UserID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		grant, err := c.Store.AccessRole(repo.ID, owner.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !policy.CanRead(owner, repo, grant) {
			return c.fail(protocol.ExitDenied, "%s belongs to %s, who cannot read %s", fp, owner.Username, repo.Path())
		}
	}
	if err := c.Store.AttachRunner(key.ID, repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "repo.runner.add", map[string]any{"repo": repo.Path(), "fingerprint": fp})
	d := map[string]string{"fingerprint": fp, "repo": repo.Path()}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "runner %s attached to %s\n", fp, repo.Path())
	})
}

func runRepoRunnerList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo runner list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	runners, err := c.Store.ListRepoRunners(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if runners == nil {
		runners = []store.RepoRunner{}
	}
	return c.emit(runners, func(w io.Writer) {
		for _, r := range runners {
			seen := r.LastSeen
			if seen == "" {
				seen = "never"
			}
			held := "idle"
			if r.BuildNumber != 0 {
				held = fmt.Sprintf("%s #%d %s since %s", r.BuildRepo, r.BuildNumber, r.BuildJob, r.StartedAt)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Fingerprint, r.Algo, r.Username, seen, held)
		}
	})
}

func runRepoRunnerRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo runner remove <owner/name> <fingerprint>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.DetachRunner(repo.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no runner %s on %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "repo.runner.remove", map[string]any{"repo": repo.Path(), "fingerprint": args[1]})
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "runner %s detached from %s\n", args[1], repo.Path())
	})
}
