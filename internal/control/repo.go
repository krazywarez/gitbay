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
		Summary: "create a repository",
		Usage:   "repo create <owner/name> [--private]", Run: runRepoCreate})
	register(Command{Path: []string{"repo", "list"},
		Summary: "list repositories you own or can access",
		Usage:   "repo list [--limit <n>] [--cursor <c>]", ReadOnly: true, Run: runRepoList})
	register(Command{Path: []string{"repo", "show"},
		Summary: "show repository details",
		Usage:   "repo show <owner/name>", ReadOnly: true, Run: runRepoShow})
	register(Command{Path: []string{"repo", "transfer"},
		Summary: "move a repository to another owner",
		Usage:   "repo transfer <owner/name> <new-owner> (clone URLs change)", Run: runRepoTransfer})
	register(Command{Path: []string{"repo", "delete"},
		Summary: "delete a repository",
		Usage:   "repo delete <owner/name> --yes", Run: runRepoDelete})
	register(Command{Path: []string{"repo", "access", "grant"},
		Summary: "grant access",
		Usage:   "repo access grant <owner/name> <user> read|write|admin", Run: runAccessGrant})
	register(Command{Path: []string{"repo", "access", "revoke"},
		Summary: "revoke access",
		Usage:   "repo access revoke <owner/name> <user>", Run: runAccessRevoke})
	register(Command{Path: []string{"repo", "access", "list"},
		Summary: "list access grants",
		Usage:   "repo access list <owner/name>", ReadOnly: true, Run: runAccessList})
	register(Command{Path: []string{"repo", "settings", "show"},
		Summary: "show settings",
		Usage:   "repo settings show <owner/name>", ReadOnly: true, Run: runSettingsShow})
	register(Command{Path: []string{"repo", "settings", "protect"},
		Summary: "protect a branch",
		Usage:   "repo settings protect <owner/name> <branch>", Run: runProtect})
	register(Command{Path: []string{"repo", "settings", "unprotect"},
		Summary: "unprotect a branch",
		Usage:   "repo settings unprotect <owner/name> <branch>", Run: runUnprotect})
	register(Command{Path: []string{"repo", "settings", "description"},
		Summary: "set the repository description",
		Usage:   "repo settings description <owner/name> <text> ('' clears)", Run: runSetDescription})
	register(Command{Path: []string{"repo", "settings", "visibility"},
		Summary: "set repository visibility",
		Usage:   "repo settings visibility <owner/name> public|private", Run: runSetVisibility})
	register(Command{Path: []string{"repo", "settings", "website"},
		Summary: "set the repository website",
		Usage:   "repo settings website <owner/name> <url> ('' clears)", Run: runSetWebsite})
	register(Command{Path: []string{"repo", "settings", "git-daemon"},
		Summary: "expose over git://",
		Usage:   "repo settings git-daemon <owner/name> on|off", Run: runGitDaemon})
	register(Command{Path: []string{"repo", "archive"},
		Summary: "archive a repository (read-only: pushes and issue/MR writes refused)",
		Usage:   "repo archive <owner/name>", Run: runArchive})
	register(Command{Path: []string{"repo", "unarchive"},
		Summary: "unarchive a repository",
		Usage:   "repo unarchive <owner/name>", Run: runUnarchive})
	register(Command{Path: []string{"repo", "topics"},
		Summary: "list topics",
		Usage:   "repo topics <owner/name>", ReadOnly: true, Run: runTopicsList})
	register(Command{Path: []string{"repo", "topics", "add"},
		Summary: "add topics",
		Usage:   "repo topics add <owner/name> <topic>...", Run: runTopicsAdd})
	register(Command{Path: []string{"repo", "topics", "remove"},
		Summary: "remove topics",
		Usage:   "repo topics remove <owner/name> <topic>...", Run: runTopicsRemove})
	register(Command{Path: []string{"repo", "search"},
		Summary: "find repositories by name, description, or topic",
		Usage:   "repo search <query>", ReadOnly: true, Run: runRepoSearch})
	register(Command{Path: []string{"repo", "grep"},
		Summary: "search file contents",
		Usage:   "repo grep <owner/name> <query> [--ref <ref>]", ReadOnly: true, Run: runRepoGrep})
	register(Command{Path: []string{"repo", "pin"},
		Summary: "pin a repository to your dashboard",
		Usage:   "repo pin <owner/name>", Run: runRepoPin})
	register(Command{Path: []string{"repo", "unpin"},
		Summary: "unpin a repository",
		Usage:   "repo unpin <owner/name>", Run: runRepoUnpin})
}

const (
	minQueryLen    = 2
	maxQueryLen    = 200
	maxGrepMatches = 200
)

func validQuery(q string) error {
	if len(q) < minQueryLen || len(q) > maxQueryLen {
		return fmt.Errorf("query must be %d to %d characters", minQueryLen, maxQueryLen)
	}
	return nil
}

// refuseArchived blocks content writes (pushes are refused in the transport
// layer) on archived repositories. Settings, access, and lifecycle commands
// stay available so an archived repo can be managed and unarchived.
func refuseArchived(c *Ctx, repo store.Repo) int {
	if repo.Settings.Archived {
		return c.fail(protocol.ExitDenied, "%s is archived and read-only", repo.Path())
	}
	return -1
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
	f, err := parseFlags(args, flagSpec{Values: []string{"--description"}, Bools: []string{"--private"}, MaxPos: 1, Usage: "repo create <owner/name> [--private] [--description <text>]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	visibility, path, description := "public", f.pos(0), f.Value("--description")
	if f.Has("--private") {
		visibility = "private"
	}
	owner, name, ok := strings.Cut(path, "/")
	if !ok {
		return c.fail(protocol.ExitUsage, "usage: repo create <owner/name> [--private]")
	}
	if err := policyValidateRepoName(name); err != nil {
		return c.failErr(err)
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
	if ownerKind == "user" {
		if code := checkRepoQuota(c); code >= 0 {
			return code
		}
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
	args, p, code := parsePageFlags(c, args, "repo", false)
	if code >= 0 {
		return code
	}
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: repo list [--limit <n>] [--cursor <c>]")
	}
	repos, err := c.Store.ListReposForUser(c.User.ID, p.queryLimit(), p.key)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	repos, next := trimPage(p, repos, "repo", store.Repo.Path)
	type out struct {
		Path        string `json:"path"`
		Visibility  string `json:"visibility"`
		Description string `json:"description,omitempty"`
		Archived    bool   `json:"archived,omitempty"`
	}
	var ds []out
	for _, r := range repos {
		desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		ds = append(ds, out{r.Path(), r.Visibility, desc, r.Settings.Archived})
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			mark := ""
			if d.Archived {
				mark = "\t[archived]"
			}
			fmt.Fprintf(w, "%s\t%s\t%s%s\n", d.Path, d.Visibility, d.Description, mark)
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
	type mirrorOut struct {
		Direction string `json:"direction"`
		URL       string `json:"url"`
		Pending   bool   `json:"pending"`
		LastSync  string `json:"last_sync,omitempty"`
		LastError string `json:"last_error,omitempty"`
	}
	type out struct {
		Path              string      `json:"path"`
		Description       string      `json:"description,omitempty"`
		Website           string      `json:"website,omitempty"`
		Visibility        string      `json:"visibility"`
		DefaultBranch     string      `json:"default_branch"`
		ProtectedBranches []string    `json:"protected_branches,omitempty"`
		Archived          bool        `json:"archived,omitempty"`
		Topics            []string    `json:"topics,omitempty"`
		Domains           []string    `json:"domains,omitempty"`
		Mirrors           []mirrorOut `json:"mirrors,omitempty"`
	}
	desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name))
	topics, err := c.Store.ListTopics(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var domains []string
	if ds, err := c.Store.ListPageDomains(repo.ID); err == nil {
		for _, pd := range ds {
			if pd.Verified() {
				domains = append(domains, pd.Domain)
			}
		}
	}
	d := out{repo.Path(), desc, repo.Settings.Website, repo.Visibility, repo.DefaultBranch,
		repo.Settings.ProtectedBranches, repo.Settings.Archived, topics, domains, nil}
	// Mirror status is admin-only, like repo mirror list. The token never
	// leaves the server.
	if grant, err := c.Store.AccessRole(repo.ID, c.User.ID); err == nil && policy.CanAdmin(c.User, repo, grant) {
		ms, err := c.Store.ListMirrors(repo.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, m := range ms {
			d.Mirrors = append(d.Mirrors, mirrorOut{m.Direction, m.URL, m.Dirty, m.LastSync, m.LastError})
		}
	}
	return c.emit(d, func(w io.Writer) {
		line := fmt.Sprintf("%s\t%s\tdefault: %s", d.Path, d.Visibility, d.DefaultBranch)
		if d.Archived {
			line += "\t[archived]"
		}
		fmt.Fprintln(w, line)
		if d.Description != "" {
			fmt.Fprintf(w, "%s\n", d.Description)
		}
		if d.Website != "" {
			fmt.Fprintf(w, "website: %s\n", d.Website)
		}
		if len(d.Topics) > 0 {
			fmt.Fprintf(w, "topics: %s\n", strings.Join(d.Topics, ", "))
		}
		if len(d.ProtectedBranches) > 0 {
			fmt.Fprintf(w, "protected: %s\n", strings.Join(d.ProtectedBranches, ", "))
		}
		if len(d.Domains) > 0 {
			fmt.Fprintf(w, "pages domains: %s\n", strings.Join(d.Domains, ", "))
		}
		for _, m := range d.Mirrors {
			status := "ok"
			if m.Pending {
				status = "pending"
			}
			if m.LastError != "" {
				status = "error: " + m.LastError
			}
			fmt.Fprintf(w, "mirror: %s %s\tlast %s\t%s\n", m.Direction, m.URL, orDash(m.LastSync), status)
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
		return c.failErr(err)
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
	// The wiki companion follows its repo.
	oldWiki := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name+".wiki")
	if _, err := os.Stat(oldWiki); err == nil {
		os.Rename(oldWiki, RepoDir(c.Cfg.Server.Root, newOwner, repo.Name+".wiki"))
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
	return deleteRepo(c, repo)
}

// deleteRepo removes a repository the caller has already been cleared to
// delete: the database row, then the directory and its wiki companion.
func deleteRepo(c *Ctx, repo store.Repo) int {
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
	os.RemoveAll(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name+".wiki"))
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
		fmt.Fprintf(w, "protected_branches: %s\nrequire_signed_commits: %v\ngit_daemon: %v\narchived: %v\n",
			strings.Join(repo.Settings.ProtectedBranches, ", "), repo.Settings.RequireSignedCommits, repo.Settings.GitDaemon, repo.Settings.Archived)
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

func runSetWebsite(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo settings website <owner/name> <url>")
	}
	site := strings.TrimSpace(args[1])
	if err := validateWebsite(site); err != nil {
		return c.failErr(err)
	}
	if len(site) > 256 {
		return c.fail(protocol.ExitUsage, "website URL too long (max 256)")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s := repo.Settings
	s.Website = site
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"website": site}, func(w io.Writer) {
		if site == "" {
			fmt.Fprintf(w, "website cleared on %s\n", repo.Path())
		} else {
			fmt.Fprintf(w, "website set on %s\n", repo.Path())
		}
	})
}

func runSetVisibility(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "public" && args[1] != "private") {
		return c.fail(protocol.ExitUsage, "usage: repo settings visibility <owner/name> public|private")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	return setRepoVisibility(c, repo, args[1])
}

// setRepoVisibility applies a visibility change the caller has already
// been cleared to make.
func setRepoVisibility(c *Ctx, repo store.Repo, visibility string) int {
	if repo.Visibility == visibility {
		return c.emit(map[string]string{"visibility": visibility}, func(w io.Writer) {
			fmt.Fprintf(w, "%s is already %s\n", repo.Path(), visibility)
		})
	}
	if err := c.Store.SetRepoVisibility(repo.ID, visibility); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// Going private takes the repository off every anonymous surface, so
	// git:// exposure cannot outlive the change.
	if visibility == "private" && repo.Settings.GitDaemon {
		s := repo.Settings
		s.GitDaemon = false
		c.Store.SetRepoSettings(repo.ID, s)
	}
	c.Store.Audit(c.User.ID, "repo.visibility", map[string]any{"repo": repo.ID, "visibility": visibility})
	return c.emit(map[string]string{"visibility": visibility}, func(w io.Writer) {
		fmt.Fprintf(w, "%s is now %s\n", repo.Path(), visibility)
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

func runArchive(c *Ctx, args []string) int   { return setArchived(c, args, true) }
func runUnarchive(c *Ctx, args []string) int { return setArchived(c, args, false) }

func setArchived(c *Ctx, args []string, archived bool) int {
	verb := "archive"
	if !archived {
		verb = "unarchive"
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo %s <owner/name>", verb)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	return archiveRepo(c, repo, archived)
}

// archiveRepo flips the archived flag on a repository the caller has
// already been cleared to manage.
func archiveRepo(c *Ctx, repo store.Repo, archived bool) int {
	verb := "archive"
	if !archived {
		verb = "unarchive"
	}
	if repo.Settings.Archived == archived {
		return c.fail(protocol.ExitUsage, "%s is already %sd", repo.Path(), verb)
	}
	s := repo.Settings
	s.Archived = archived
	if err := c.Store.SetRepoSettings(repo.ID, s); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "repo."+verb+"d", "{}")
	return c.emit(s, func(w io.Writer) { fmt.Fprintf(w, "%sd %s\n", verb, repo.Path()) })
}

func runTopicsList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo topics <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	topics, err := c.Store.ListTopics(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(topics, func(w io.Writer) {
		for _, t := range topics {
			fmt.Fprintln(w, t)
		}
	})
}

func runTopicsAdd(c *Ctx, args []string) int    { return editTopics(c, args, true) }
func runTopicsRemove(c *Ctx, args []string) int { return editTopics(c, args, false) }

func editTopics(c *Ctx, args []string, add bool) int {
	verb := "add"
	if !add {
		verb = "remove"
	}
	if len(args) < 2 {
		return c.fail(protocol.ExitUsage, "usage: repo topics %s <owner/name> <topic>...", verb)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	topics := args[1:]
	if add {
		for _, t := range topics {
			if err := policy.ValidateTopic(t); err != nil {
				return c.failErr(err)
			}
		}
		have, err := c.Store.ListTopics(repo.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		added := 0
		for _, t := range topics {
			if !slices.Contains(have, t) {
				added++
			}
		}
		if len(have)+added > policy.MaxTopics {
			return c.fail(protocol.ExitUsage, "a repository can have at most %d topics", policy.MaxTopics)
		}
		for _, t := range topics {
			if err := c.Store.AddTopic(repo.ID, t); err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
		}
	} else {
		for _, t := range topics {
			if err := c.Store.RemoveTopic(repo.ID, t); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return c.fail(protocol.ExitNotFound, "%s has no topic %q", repo.Path(), t)
				}
				return c.fail(protocol.ExitFailure, "%v", err)
			}
		}
	}
	now, err := c.Store.ListTopics(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(now, func(w io.Writer) {
		fmt.Fprintf(w, "topics on %s: %s\n", repo.Path(), strings.Join(now, ", "))
	})
}

// runRepoSearch matches the query against name, owner/name, description,
// and topics of every repository the caller can see.
func runRepoSearch(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo search <query>")
	}
	if err := validQuery(args[0]); err != nil {
		return c.failErr(err)
	}
	q := strings.ToLower(args[0])

	public, err := c.Store.ListPublicRepos()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	own, err := c.Store.ListReposForUser(c.User.ID, 0, "")
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	seen := map[int64]bool{}
	type out struct {
		Path        string   `json:"path"`
		Visibility  string   `json:"visibility"`
		Description string   `json:"description,omitempty"`
		Topics      []string `json:"topics,omitempty"`
	}
	var ds []out
	for _, r := range append(public, own...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		desc := gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		topics, _ := c.Store.ListTopics(r.ID)
		if !matchesRepo(q, r, desc, topics) {
			continue
		}
		ds = append(ds, out{r.Path(), r.Visibility, desc, topics})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\t%s\n", d.Path, d.Visibility, d.Description)
		}
	})
}

func matchesRepo(q string, r store.Repo, desc string, topics []string) bool {
	if strings.Contains(strings.ToLower(r.Path()), q) ||
		strings.Contains(strings.ToLower(desc), q) {
		return true
	}
	for _, t := range topics {
		if strings.Contains(t, q) {
			return true
		}
	}
	return false
}

func runRepoGrep(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--ref"}, MaxPos: 2, Usage: "repo grep <owner/name> <query> [--ref <ref>]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, query, ref := f.pos(0), f.pos(1), f.Value("--ref")
	if path == "" || query == "" {
		return c.fail(protocol.ExitUsage, "usage: repo grep <owner/name> <query> [--ref <ref>]")
	}
	if err := validQuery(query); err != nil {
		return c.failErr(err)
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.ResolveRef(dir, ref); err != nil {
		return c.fail(protocol.ExitNotFound, "no ref %q in %s", ref, repo.Path())
	}
	matches, err := gitutil.Grep(dir, ref, query, maxGrepMatches)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var ds []out
	for _, m := range matches {
		ds = append(ds, out{m.Path, m.Line, m.Text})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s:%d:%s\n", d.Path, d.Line, d.Text)
		}
	})
}

func runRepoPin(c *Ctx, args []string) int   { return setPinned(c, args, true) }
func runRepoUnpin(c *Ctx, args []string) int { return setPinned(c, args, false) }

func setPinned(c *Ctx, args []string, pin bool) int {
	verb := "pin"
	if !pin {
		verb = "unpin"
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo %s <owner/name>", verb)
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	if pin {
		if err := c.Store.PinRepo(c.User.ID, repo.ID); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	} else if err := c.Store.UnpinRepo(c.User.ID, repo.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%s is not pinned", repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{verb + "ned": repo.Path()}, func(w io.Writer) {
		fmt.Fprintf(w, "%sned %s\n", verb, repo.Path())
	})
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
