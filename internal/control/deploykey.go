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

func init() {
	register(Command{Path: []string{"repo", "deploy-key", "add"},
		Summary:    "bind a read-only (or --rw) key to one repository: repo deploy-key add <owner/name> [--rw] < key.pub",
		ReadsStdin: true, Run: runDeployKeyAdd})
	register(Command{Path: []string{"repo", "deploy-key", "list"},
		Summary: "list deploy keys: repo deploy-key list <owner/name>", ReadOnly: true, Run: runDeployKeyList})
	register(Command{Path: []string{"repo", "deploy-key", "remove"},
		Summary: "remove a deploy key: repo deploy-key remove <owner/name> <fingerprint>", Run: runDeployKeyRemove})
}

func runDeployKeyAdd(c *Ctx, args []string) int {
	mode := "ro"
	var path string
	for _, a := range args {
		switch a {
		case "--rw":
			mode = "rw"
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "usage: repo deploy-key add <owner/name> [--rw] < key.pub")
			}
			path = a
		}
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: repo deploy-key add <owner/name> [--rw] < key.pub")
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
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
	scope := fmt.Sprintf("deploy:%d:%s", repo.ID, mode)
	if err := c.Store.AddSSHKey(c.User.ID, fp, pub.Type(), pub.Marshal(), scope); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.fail(protocol.ExitUsage, "%v", err)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"fingerprint": fp, "mode": mode}, func(w io.Writer) {
		fmt.Fprintf(w, "deploy key %s (%s) bound to %s\n", fp, mode, repo.Path())
	})
}

func runDeployKeyList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo deploy-key list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	keys, err := c.Store.ListDeployKeys(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Fingerprint string `json:"fingerprint"`
		Algo        string `json:"algo"`
		Mode        string `json:"mode"`
	}
	var ds []out
	for _, k := range keys {
		mode := "ro"
		if policy.DeployScopeAllows(k.Scope, repo.ID, true) {
			mode = "rw"
		}
		ds = append(ds, out{k.Fingerprint, k.Algo, mode})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Fingerprint, d.Algo, d.Mode)
		}
	})
}

func runDeployKeyRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo deploy-key remove <owner/name> <fingerprint>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.RemoveDeployKey(repo.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no deploy key %s on %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed deploy key %s from %s\n", args[1], repo.Path())
	})
}
