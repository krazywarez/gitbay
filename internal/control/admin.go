package control

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
	return c.emit(map[string]any{"user": u.Username, "admin": admin}, func(w io.Writer) {
		fmt.Fprintf(w, "%sd %s\n", verb, u.Username)
	})
}
