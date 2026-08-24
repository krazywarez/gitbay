package control

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// RepoDir returns the on-disk path for a repository.
func RepoDir(root, owner, name string) string {
	return filepath.Join(root, "repos", owner, name+".git")
}

// HooksDir is the shared core.hooksPath directory.
func HooksDir(root string) string { return filepath.Join(root, "hooks") }

func init() {
	register(Command{Path: []string{"repo", "create"},
		Summary: "create a repository: repo create <owner/name> [--private]", Run: runRepoCreate})
	register(Command{Path: []string{"repo", "list"},
		Summary: "list repositories you own or can access", ReadOnly: true, Run: runRepoList})
	register(Command{Path: []string{"repo", "show"},
		Summary: "show repository details: repo show <owner/name>", ReadOnly: true, Run: runRepoShow})
	register(Command{Path: []string{"repo", "transfer"},
		Summary: "move a repository to another owner: repo transfer <owner/name> <new-owner> (clone URLs change)", Run: runRepoTransfer})
	register(Command{Path: []string{"repo", "delete"},
		Summary: "delete a repository: repo delete <owner/name> --yes", Run: runRepoDelete})
	register(Command{Path: []string{"repo", "access", "grant"},
		Summary: "grant access: repo access grant <owner/name> <user> read|write|admin", Run: runAccessGrant})
	register(Command{Path: []string{"repo", "access", "revoke"},
		Summary: "revoke access: repo access revoke <owner/name> <user>", Run: runAccessRevoke})
	register(Command{Path: []string{"repo", "access", "list"},
		Summary: "list access grants: repo access list <owner/name>", ReadOnly: true, Run: runAccessList})
	register(Command{Path: []string{"repo", "settings", "show"},
		Summary: "show settings: repo settings show <owner/name>", ReadOnly: true, Run: runSettingsShow})
	register(Command{Path: []string{"repo", "settings", "protect"},
		Summary: "protect a branch: repo settings protect <owner/name> <branch>", Run: runProtect})
	register(Command{Path: []string{"repo", "settings", "unprotect"},
		Summary: "unprotect a branch: repo settings unprotect <owner/name> <branch>", Run: runUnprotect})
	register(Command{Path: []string{"repo", "settings", "description"},
		Summary: "set the repository description: repo settings description <owner/name> <text> ('' clears)", Run: runSetDescription})
	register(Command{Path: []string{"repo", "settings", "git-daemon"},
		Summary: "expose over git://: repo settings git-daemon <owner/name> on|off", Run: runGitDaemon})
}

// resolveRepo loads a repo and checks the given permission for c.User.
func resolveRepo(c *Ctx, path string, check func(store.User, store.Repo, string) bool) (store.Repo, int) {
	repo, err := c.Store.RepoByPath(path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Same message whether it doesn't exist or is invisible.
			return repo, c.fail(protocol.ExitNotFound, "repository %s not found", path)
		}
		return repo, c.fail(protocol.ExitFailure, "loading repository: %v", err)
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return repo, c.fail(protocol.ExitFailure, "checking access: %v", err)
	}
	if !check(c.User, repo, grant) {
		if !policy.CanRead(c.User, repo, grant) {
			// Invisible repos 404, per the enumeration rule.
			return repo, c.fail(protocol.ExitNotFound, "repository %s not found", path)
		}
		return repo, c.fail(protocol.ExitDenied, "permission denied on %s", path)
	}
	return repo, -1
}

func runRepoCreate(c *Ctx, args []string) int {
	visibility := "public"
	var path, description string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--private":
			visibility = "private"
		case "--description":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--description requires a value")
			}
			description = args[i+1]
			i++
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "usage: repo create <owner/name> [--private] [--description <text>]")
			}
			path = args[i]
		}
	}
	owner, name, ok := strings.Cut(path, "/")
	if !ok {
		return c.fail(protocol.ExitUsage, "usage: repo create <owner/name> [--private]")
	}
	if err := policyValidateRepoName(name); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	ownerKind, ownerID := "user", c.User.ID
	if owner != c.User.Username {
		org, err := c.Store.OrgByName(owner)
		if err != nil {
			return c.fail(protocol.ExitDenied, "cannot create repositories under %q: not you and not an organization you can see", owner)
		}
		role, err := c.Store.OrgRole(org.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if role != "admin" {
			return c.fail(protocol.ExitDenied, "only admins of %s can create repositories there", owner)
		}
		ownerKind, ownerID = "org", org.ID
	}
	id, err := c.Store.CreateRepo(ownerKind, ownerID, name, visibility)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	dir := RepoDir(c.Cfg.Server.Root, owner, name)
	if err := gitutil.InitBare(dir, "main", HooksDir(c.Cfg.Server.Root)); err != nil {
		c.Store.DeleteRepo(id)
		return c.fail(protocol.ExitFailure, "initializing repository: %v", err)
	}
	if description != "" {
		if err := gitutil.WriteDescription(dir, description); err != nil {
			return c.fail(protocol.ExitFailure, "writing description: %v", err)
		}
	}
	type out struct {
		Path       string `json:"path"`
		Visibility string `json:"visibility"`
		SSHURL     string `json:"ssh_url"`
	}
	d := out{Path: path, Visibility: visibility, SSHURL: "ssh://git@" + hostOf(c.Cfg.Server.SiteURL) + "/" + path + ".git"}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "created %s (%s)\nclone: git clone %s\n", d.Path, d.Visibility, d.SSHURL)
	})
}

func policyValidateRepoName(name string) error { return policy.ValidateName(name) }

func hostOf(siteURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(siteURL, "https://"), "http://")
	return strings.TrimSuffix(s, "/")
}

func runRepoList(c *Ctx, args []string) int {
	repos, err := c.Store.ListReposForUser(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Path        string `json:"path"`
		Visibility  string `json:"visibility"`
		Description string `json:"description,omitempty"`
	}
	var ds []out
	for _, r := range repos {
		desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		ds = append(ds, out{r.Path(), r.Visibility, desc})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Path, d.Visibility, d.Description)
		}
	})
}

func runRepoShow(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo show <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	type out struct {
		Path              string   `json:"path"`
		Description       string   `json:"description,omitempty"`
		Visibility        string   `json:"visibility"`
		DefaultBranch     string   `json:"default_branch"`
		ProtectedBranches []string `json:"protected_branches,omitempty"`
	}
	desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name))
	d := out{repo.Path(), desc, repo.Visibility, repo.DefaultBranch, repo.Settings.ProtectedBranches}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\t%s\tdefault: %s\n", d.Path, d.Visibility, d.DefaultBranch)
		if d.Description != "" {
			fmt.Fprintf(w, "%s\n", d.Description)
		}
		if len(d.ProtectedBranches) > 0 {
			fmt.Fprintf(w, "protected: %s\n", strings.Join(d.ProtectedBranches, ", "))
		}
	})
}

func runRepoTransfer(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo transfer <owner/name> <new-owner>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	newOwner := args[1]
	if newOwner == repo.OwnerName {
		return c.fail(protocol.ExitUsage, "%s already owns this repository", newOwner)
	}

	// Target: yourself, or an org you admin — same rule as repo create.
	newKind, newID := "", int64(0)
	if newOwner == c.User.Username {
		newKind, newID = "user", c.User.ID
	} else if org, err := c.Store.OrgByName(newOwner); err == nil {
		role, err := c.Store.OrgRole(org.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if role != "admin" {
			return c.fail(protocol.ExitDenied, "only admins of %s can receive repositories there", newOwner)
		}
		newKind, newID = "org", org.ID
	} else {
		return c.fail(protocol.ExitDenied, "cannot transfer to %q: not you and not an organization you can see", newOwner)
	}

	oldDir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	newDir := RepoDir(c.Cfg.Server.Root, newOwner, repo.Name)
	if _, err := os.Stat(newDir); err == nil {
		return c.fail(protocol.ExitFailure, "repository directory already exists at %s/%s", newOwner, repo.Name)
	}
	if err := c.Store.TransferRepo(repo.ID, newKind, newID); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o750); err != nil {
		c.Store.TransferRepo(repo.ID, repo.OwnerKind, repo.OwnerID)
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		// Keep name and disk consistent: revert the database change.
		c.Store.TransferRepo(repo.ID, repo.OwnerKind, repo.OwnerID)
		return c.fail(protocol.ExitFailure, "moving repository: %v", err)
	}
	newPath := newOwner + "/" + repo.Name
	return c.emit(map[string]string{"repo": newPath, "was": repo.Path()}, func(w io.Writer) {
		fmt.Fprintf(w, "transferred %s to %s — clone URLs now use %s\n", repo.Path(), newPath, newPath)
	})
}

func runRepoDelete(c *Ctx, args []string) int {
	var path string
	var yes bool
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else if path == "" {
			path = a
		} else {
			return c.fail(protocol.ExitUsage, "usage: repo delete <owner/name> --yes")
		}
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: repo delete <owner/name> --yes")
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if !yes {
		return c.fail(protocol.ExitUsage, "repo delete is permanent; re-run with --yes")
	}
	// Open MRs sourced from this repo keep working (targets own the
	// objects) but must show that the source is gone.
	if err := c.Store.MarkSourceGoneForRepo(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.DeleteRepo(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := os.RemoveAll(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)); err != nil {
		return c.fail(protocol.ExitFailure, "database row removed but disk cleanup failed: %v", err)
	}
	return c.emit(map[string]string{"deleted": repo.Path()}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted %s\n", repo.Path())
	})
}

func runAccessGrant(c *Ctx, args []string) int {
	if len(args) != 3 || !slices.Contains([]string{"read", "write", "admin"}, args[2]) {
		return c.fail(protocol.ExitUsage, "usage: repo access grant <owner/name> <user> read|write|admin")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	target, err := c.Store.UserByUsername(args[1])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such user %q", args[1])
	}
	if err := c.Store.GrantAccess(repo.ID, target.ID, args[2]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"granted": args[2], "user": target.Username},
		func(w io.Writer) { fmt.Fprintf(w, "granted %s to %s on %s\n", args[2], target.Username, repo.Path()) })
}

func runAccessRevoke(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo access revoke <owner/name> <user>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	target, err := c.Store.UserByUsername(args[1])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such user %q", args[1])
	}
	if err := c.Store.RevokeAccess(repo.ID, target.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%s has no grant on %s", target.Username, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"revoked": target.Username},
		func(w io.Writer) { fmt.Fprintf(w, "revoked %s on %s\n", target.Username, repo.Path()) })
}

func runAccessList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo access list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	entries, err := c.Store.ListAccess(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		User string `json:"user"`
		Role string `json:"role"`
	}
	var ds []out
	for _, e := range entries {
		ds = append(ds, out{e.Username, e.Role})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\n", d.User, d.Role)
		}
	})
}

func runSettingsShow(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo settings show <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	return c.emit(repo.Settings, func(w io.Writer) {
		fmt.Fprintf(w, "protected_branches: %s\nrequire_signed_commits: %v\ngit_daemon: %v\n",
			strings.Join(repo.Settings.ProtectedBranches, ", "), repo.Settings.RequireSignedCommits, repo.Settings.GitDaemon)
	})
}

func runSetDescription(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo settings description <owner/name> <text>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if err := gitutil.WriteDescription(dir, args[1]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"description": gitutil.ReadDescription(dir)}, func(w io.Writer) {
		fmt.Fprintf(w, "description set on %s\n", repo.Path())
	})
}

func runGitDaemon(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return c.fail(protocol.ExitUsage, "usage: repo settings git-daemon <owner/name> on|off")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	on := args[1] == "on"
	if on && repo.Visibility != "public" {
		return c.fail(protocol.ExitUsage, "git:// serves only public repositories; %s is private", repo.Path())
	}
	if on && !c.Cfg.GitDaemon.Enabled {
		return c.fail(protocol.ExitUsage, "this instance does not run the git:// daemon ([git_daemon] enabled = false)")
	}
	s := repo.Settings
	s.GitDaemon = on
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) { fmt.Fprintf(w, "git-daemon %s on %s\n", args[1], repo.Path()) })
}

func runProtect(c *Ctx, args []string) int   { return setProtect(c, args, true) }
func runUnprotect(c *Ctx, args []string) int { return setProtect(c, args, false) }

func setProtect(c *Ctx, args []string, protect bool) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo settings protect|unprotect <owner/name> <branch>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	branch := args[1]
	s := repo.Settings
	has := slices.Contains(s.ProtectedBranches, branch)
	if protect && !has {
		s.ProtectedBranches = append(s.ProtectedBranches, branch)
		slices.Sort(s.ProtectedBranches)
	}
	if !protect && has {
		s.ProtectedBranches = slices.DeleteFunc(s.ProtectedBranches, func(b string) bool { return b == branch })
	}
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	verb := "protected"
	if !protect {
		verb = "unprotected"
	}
	return c.emit(s, func(w io.Writer) { fmt.Fprintf(w, "%s %s on %s\n", verb, branch, repo.Path()) })
}
