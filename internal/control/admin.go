package control

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"admin", "user", "list"},
		Summary:  "list accounts (instance admins)",
		Usage:    "admin user list [--state active|pending|disabled|admin] [--limit <n>] [--cursor <c>]",
		ReadOnly: true, SSHOnly: true, Run: runAdminUserList})
	register(Command{Path: []string{"admin", "user", "show"},
		Summary:  "show an account: keys, emails, orgs, tokens, sessions (instance admins)",
		Usage:    "admin user show <username>",
		ReadOnly: true, SSHOnly: true, Run: runAdminUserShow})
	register(Command{Path: []string{"admin", "user", "promote"},
		Summary: "make an account an instance admin",
		Usage:   "admin user promote <username>",
		SSHOnly: true, Run: runAdminUserPromote})
	register(Command{Path: []string{"admin", "user", "demote"},
		Summary: "remove instance admin from an account (never the last one)",
		Usage:   "admin user demote <username>",
		SSHOnly: true, Run: runAdminUserDemote})
	register(Command{Path: []string{"admin", "runners"},
		Summary:  "runner accounts: last poll, scope, the build each holds (instance admins)",
		Usage:    "admin runners",
		ReadOnly: true, SSHOnly: true, Run: runAdminRunners})
	register(Command{Path: []string{"admin", "repo", "list"},
		Summary:  "list every repository with size and last push (instance admins)",
		Usage:    "admin repo list [--owner <name>] [--visibility public|private] [--limit <n>] [--cursor <c>]",
		ReadOnly: true, SSHOnly: true, Run: runAdminRepoList})
	register(Command{Path: []string{"admin", "repo", "archive"},
		Summary: "archive any repository (instance admins; audited)",
		Usage:   "admin repo archive <owner/name>",
		SSHOnly: true, Run: runAdminRepoArchive})
	register(Command{Path: []string{"admin", "repo", "unarchive"},
		Summary: "unarchive any repository (instance admins; audited)",
		Usage:   "admin repo unarchive <owner/name>",
		SSHOnly: true, Run: runAdminRepoUnarchive})
	register(Command{Path: []string{"admin", "repo", "visibility"},
		Summary: "set any repository's visibility (instance admins; audited)",
		Usage:   "admin repo visibility <owner/name> public|private",
		SSHOnly: true, Run: runAdminRepoVisibility})
	register(Command{Path: []string{"admin", "repo", "delete"},
		Summary: "delete any repository (instance admins; audited)",
		Usage:   "admin repo delete <owner/name> --yes",
		SSHOnly: true, Run: runAdminRepoDelete})
}

// requireInstanceAdmin gates the admin noun. -1 means proceed.
func requireInstanceAdmin(c *Ctx) int {
	if !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "admin commands are for instance admins")
	}
	return -1
}

// adminUserOut is one account row, shared by list and show.
type adminUserOut struct {
	Username  string `json:"username"`
	State     string `json:"state"` // active | pending | disabled
	Admin     bool   `json:"admin"`
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen,omitempty"`
}

func adminUserRow(u store.AdminUser) adminUserOut {
	state := "active"
	switch {
	case u.Disabled:
		state = "disabled"
	case u.Pending:
		state = "pending"
	}
	return adminUserOut{u.Username, state, u.IsAdmin, u.CreatedAt, u.LastSeen}
}

func runAdminUserList(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	args, p, code := parsePageFlags(c, args, "admin-user", false)
	if code >= 0 {
		return code
	}
	state := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--state":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--state requires active|pending|disabled|admin")
			}
			state = args[i+1]
			i++
		default:
			return c.fail(protocol.ExitUsage, "usage: admin user list [--state active|pending|disabled|admin] [--limit <n>] [--cursor <c>]")
		}
	}
	switch state {
	case "", "active", "pending", "disabled", "admin":
	default:
		return c.fail(protocol.ExitUsage, "--state requires active|pending|disabled|admin")
	}
	users, err := c.Store.ListUsers(state, p.queryLimit(), p.key)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	users, next := trimPage(p, users, "admin-user", func(u store.AdminUser) string { return u.Username })
	var ds []adminUserOut
	for _, u := range users {
		ds = append(ds, adminUserRow(u))
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			mark := ""
			if d.Admin {
				mark = "admin"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Username, d.State, mark, d.CreatedAt, d.LastSeen)
		}
	})
}

func runAdminUserShow(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: admin user show <username>")
	}
	name := args[0]
	u, err := c.Store.UserByUsername(name)
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no user %q", name)
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	row, err := c.Store.AdminUserByName(name)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	type keyOut struct {
		Fingerprint string `json:"fingerprint"`
		Algo        string `json:"algo"`
		Scope       string `json:"scope"`
		CreatedAt   string `json:"created_at"`
		LastUsedAt  string `json:"last_used_at,omitempty"`
	}
	type emailOut struct {
		Address    string `json:"address"`
		Verified   bool   `json:"verified"`
		VerifiedBy string `json:"verified_by,omitempty"` // smtp | admin
		Primary    bool   `json:"primary"`
	}
	type pgpOut struct {
		Fingerprint string     `json:"fingerprint"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
		RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	}
	type orgOut struct {
		Org  string `json:"org"`
		Role string `json:"role"`
	}
	type tokenOut struct {
		Name       string     `json:"name"`
		Scope      string     `json:"scope"`
		CreatedAt  string     `json:"created_at"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	}
	type out struct {
		adminUserOut
		Keys        []keyOut   `json:"keys"`
		Emails      []emailOut `json:"emails"`
		PGPKeys     []pgpOut   `json:"pgp_keys"`
		Orgs        []orgOut   `json:"orgs"`
		Repos       int64      `json:"repos"`
		APITokens   []tokenOut `json:"api_tokens"`
		WebSessions int64      `json:"web_sessions"`
	}
	d := out{adminUserOut: adminUserRow(row),
		Keys: []keyOut{}, Emails: []emailOut{}, PGPKeys: []pgpOut{}, Orgs: []orgOut{}, APITokens: []tokenOut{}}

	keys, err := c.Store.ListSSHKeys(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, k := range keys {
		d.Keys = append(d.Keys, keyOut{k.Fingerprint, k.Algo, k.Scope, k.CreatedAt, k.LastUsedAt})
	}
	emails, err := c.Store.ListEmails(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, e := range emails {
		d.Emails = append(d.Emails, emailOut{e.Address, e.Verified, e.VerifiedBy, e.Primary})
	}
	pgp, err := c.Store.ListPGPKeys(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, k := range pgp {
		d.PGPKeys = append(d.PGPKeys, pgpOut{k.Fingerprint, k.ExpiresAt, k.RevokedAt})
	}
	orgs, err := c.Store.ListOrgsForUser(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, m := range orgs {
		d.Orgs = append(d.Orgs, orgOut{m.Username, m.Role})
	}
	if d.Repos, err = c.Store.OwnedRepoCount(u.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	tokens, err := c.Store.ListAPITokens(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, t := range tokens {
		d.APITokens = append(d.APITokens, tokenOut{t.Name, t.Scope, t.CreatedAt, t.ExpiresAt, t.LastUsedAt})
	}
	if d.WebSessions, err = c.Store.WebSessionCount(u.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\t%s", d.Username, d.State)
		if d.Admin {
			fmt.Fprint(w, "\tadmin")
		}
		fmt.Fprintf(w, "\ncreated\t%s\n", d.CreatedAt)
		if d.LastSeen != "" {
			fmt.Fprintf(w, "last seen\t%s\n", d.LastSeen)
		}
		fmt.Fprintf(w, "repos\t%d\nweb sessions\t%d\n", d.Repos, d.WebSessions)
		fmt.Fprintln(w, "keys:")
		for _, k := range d.Keys {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", k.Fingerprint, k.Algo, k.Scope, k.LastUsedAt)
		}
		fmt.Fprintln(w, "emails:")
		for _, e := range d.Emails {
			state := "unverified"
			if e.Verified {
				state = "verified by " + e.VerifiedBy
			}
			mark := ""
			if e.Primary {
				mark = "\tprimary"
			}
			fmt.Fprintf(w, "  %s\t%s%s\n", e.Address, state, mark)
		}
		fmt.Fprintln(w, "pgp keys:")
		for _, k := range d.PGPKeys {
			fmt.Fprintf(w, "  %s\n", k.Fingerprint)
		}
		fmt.Fprintln(w, "orgs:")
		for _, o := range d.Orgs {
			fmt.Fprintf(w, "  %s\t%s\n", o.Org, o.Role)
		}
		fmt.Fprintln(w, "api tokens:")
		for _, t := range d.APITokens {
			used := ""
			if t.LastUsedAt != nil {
				used = t.LastUsedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", t.Name, t.Scope, strings.TrimSpace(used))
		}
	})
}

func runAdminUserPromote(c *Ctx, args []string) int { return setAdmin(c, args, true) }
func runAdminUserDemote(c *Ctx, args []string) int  { return setAdmin(c, args, false) }

func setAdmin(c *Ctx, args []string, admin bool) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	verb := "demote"
	if admin {
		verb = "promote"
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: admin user %s <username>", verb)
	}
	u, err := c.Store.UserByUsername(args[0])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no user %q", args[0])
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if u.IsAdmin == admin {
		return c.fail(protocol.ExitUsage, "%s is already %s", u.Username, map[bool]string{true: "an admin", false: "not an admin"}[admin])
	}
	if admin && (u.Pending || u.Disabled) {
		return c.fail(protocol.ExitUsage, "%s is %s; only an active account can be an admin", u.Username,
			map[bool]string{true: "disabled", false: "pending"}[u.Disabled])
	}
	if err := c.Store.SetUserAdmin(u.ID, admin); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			return c.fail(protocol.ExitUsage, "%v", err)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "admin user."+verb+"d", map[string]any{"user": u.Username})
	return c.emit(map[string]any{"user": u.Username, "admin": admin}, func(w io.Writer) {
		fmt.Fprintf(w, "%sd %s\n", verb, u.Username)
	})
}

// adminRepo loads a repository for an admin override. Instance admin
// carries no implicit read right, so policy is not consulted; the only
// refusal is a path that does not exist. Every caller audits what it does.
func adminRepo(c *Ctx, path string) (store.Repo, int) {
	if code := requireInstanceAdmin(c); code >= 0 {
		return store.Repo{}, code
	}
	repo, err := c.Store.RepoByPath(path)
	if errors.Is(err, store.ErrNotFound) {
		return repo, c.fail(protocol.ExitNotFound, "repository %s not found", path)
	} else if err != nil {
		return repo, c.fail(protocol.ExitFailure, "loading repository: %v", err)
	}
	return repo, -1
}

func runAdminRepoList(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	args, p, code := parsePageFlags(c, args, "admin-repo", false)
	if code >= 0 {
		return code
	}
	var owner, visibility string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--owner":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--owner requires a value")
			}
			owner = args[i+1]
			i++
		case "--visibility":
			if i+1 >= len(args) || (args[i+1] != "public" && args[i+1] != "private") {
				return c.fail(protocol.ExitUsage, "--visibility requires public|private")
			}
			visibility = args[i+1]
			i++
		default:
			return c.fail(protocol.ExitUsage, "usage: admin repo list [--owner <name>] [--visibility public|private] [--limit <n>] [--cursor <c>]")
		}
	}
	repos, err := c.Store.ListReposAdmin(owner, visibility, p.queryLimit(), p.key)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	repos, next := trimPage(p, repos, "admin-repo", func(r store.AdminRepo) string { return r.Path })
	type out struct {
		Path       string `json:"path"`
		Visibility string `json:"visibility"`
		Archived   bool   `json:"archived,omitempty"`
		CreatedAt  string `json:"created_at"`
		LastPush   string `json:"last_push,omitempty"`
		Bytes      int64  `json:"bytes"`
	}
	var ds []out
	for _, r := range repos {
		size := gitutil.DirSize(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		ds = append(ds, out{r.Path, r.Visibility, r.Archived, r.CreatedAt, r.LastPush, size})
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			mark := ""
			if d.Archived {
				mark = "\t[archived]"
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s%s\n", d.Path, d.Visibility, d.Bytes, d.CreatedAt, d.LastPush, mark)
		}
	})
}

func runAdminRepoArchive(c *Ctx, args []string) int   { return adminArchive(c, args, true) }
func runAdminRepoUnarchive(c *Ctx, args []string) int { return adminArchive(c, args, false) }

func adminArchive(c *Ctx, args []string, archived bool) int {
	verb := "archive"
	if !archived {
		verb = "unarchive"
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: admin repo %s <owner/name>", verb)
	}
	repo, code := adminRepo(c, args[0])
	if code >= 0 {
		return code
	}
	if code := archiveRepo(c, repo, archived); code != protocol.ExitOK {
		return code
	}
	c.Store.Audit(c.User.ID, "admin repo."+verb, map[string]any{"repo": repo.Path()})
	return protocol.ExitOK
}

func runAdminRepoVisibility(c *Ctx, args []string) int {
	if len(args) != 2 || (args[1] != "public" && args[1] != "private") {
		return c.fail(protocol.ExitUsage, "usage: admin repo visibility <owner/name> public|private")
	}
	repo, code := adminRepo(c, args[0])
	if code >= 0 {
		return code
	}
	if code := setRepoVisibility(c, repo, args[1]); code != protocol.ExitOK {
		return code
	}
	c.Store.Audit(c.User.ID, "admin repo.visibility", map[string]any{"repo": repo.Path(), "visibility": args[1]})
	return protocol.ExitOK
}

func runAdminRepoDelete(c *Ctx, args []string) int {
	var path string
	var yes bool
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else if path == "" {
			path = a
		} else {
			return c.fail(protocol.ExitUsage, "usage: admin repo delete <owner/name> --yes")
		}
	}
	if path == "" {
		return c.fail(protocol.ExitUsage, "usage: admin repo delete <owner/name> --yes")
	}
	repo, code := adminRepo(c, path)
	if code >= 0 {
		return code
	}
	if !yes {
		return c.fail(protocol.ExitUsage, "admin repo delete is permanent; re-run with --yes")
	}
	if code := deleteRepo(c, repo); code != protocol.ExitOK {
		return code
	}
	c.Store.Audit(c.User.ID, "admin repo.delete", map[string]any{"repo": repo.Path()})
	return protocol.ExitOK
}

func runAdminRunners(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: admin runners")
	}
	runners, err := c.Store.ListRunners()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(runners, func(w io.Writer) {
		for _, r := range runners {
			scope := r.Scope
			if scope == "" {
				scope = "any"
			}
			held := "idle"
			if r.BuildNumber != 0 {
				held = fmt.Sprintf("%s #%d %s since %s", r.BuildRepo, r.BuildNumber, r.BuildJob, r.StartedAt)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Username, r.LastSeen, scope, held)
		}
	})
}
