package control

import (
	"fmt"
	"io"
	"strconv"
	"sync"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Quotas cap what a user or an org owns directly, and how many orgs an
// account creates. The limit is the owner's override when set, else the
// configured default; 0 is unlimited.

// RepoLimit is the owner's repository cap, 0 for none.
func RepoLimit(st *store.Store, cfg configLimits, kind string, id int64) int64 {
	if l, err := st.OwnerLimits(kind, id); err == nil && l.Repos != nil {
		return *l.Repos
	}
	if kind == "org" {
		return int64(cfg.MaxReposPerOrg)
	}
	return int64(cfg.MaxReposPerUser)
}

// ByteLimit is the owner's storage cap in bytes, 0 for none.
func ByteLimit(st *store.Store, cfg configLimits, kind string, id int64) int64 {
	if l, err := st.OwnerLimits(kind, id); err == nil && l.Bytes != nil {
		return *l.Bytes
	}
	if kind == "org" {
		return cfg.MaxBytesPerOrg
	}
	return cfg.MaxBytesPerUser
}

// OrgLimit is the account's cap on organizations it creates, 0 for none.
func OrgLimit(st *store.Store, cfg configLimits, userID int64) int64 {
	if l, err := st.OwnerLimits("user", userID); err == nil && l.Orgs != nil {
		return *l.Orgs
	}
	return int64(cfg.MaxOrgsPerUser)
}

// OwnedBytes is the disk taken by the repositories an owner holds directly.
func OwnedBytes(st *store.Store, root, kind string, id int64) int64 {
	repos, err := st.ListReposForOwner(kind, id)
	if err != nil {
		return 0
	}
	var total int64
	for _, r := range repos {
		total += gitutil.DirSize(RepoDir(root, r.OwnerName, r.Name))
	}
	return total
}

// configLimits is the slice of config the quota functions read, so the
// sshd package can pass its Limits without importing control's Ctx.
type configLimits struct {
	MaxReposPerUser int
	MaxBytesPerUser int64
	MaxOrgsPerUser  int
	MaxReposPerOrg  int
	MaxBytesPerOrg  int64
}

// QuotaConfig is what sshd passes: the limits section of the config.
func QuotaConfig(cfg config.Config) configLimits {
	l := cfg.Limits
	return configLimits{l.MaxReposPerUser, l.MaxBytesPerUser, l.MaxOrgsPerUser, l.MaxReposPerOrg, l.MaxBytesPerOrg}
}

func limitsOf(c *Ctx) configLimits { return QuotaConfig(c.Cfg) }

// checkRepoQuota refuses one more repository for the owner past its cap.
// repoCreateMu serialises the quota check with the insert that follows
// it, so two concurrent creates cannot both pass the count (#108). One
// process serves the instance, so a process-wide lock is the whole story.
var repoCreateMu sync.Mutex

func checkRepoQuota(c *Ctx, kind string, id int64) int {
	limit := RepoLimit(c.Store, limitsOf(c), kind, id)
	if limit == 0 {
		return -1
	}
	n, err := c.Store.OwnedRepoCount(kind, id)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if n >= limit {
		if kind == "org" {
			return c.fail(protocol.ExitDenied, "the organization owns %d of the %d repositories it may hold; delete or transfer one, or ask an admin to raise the limit", n, limit)
		}
		return c.fail(protocol.ExitDenied, "you own %d of the %d repositories your account may hold; delete or transfer one, or ask an admin to raise the limit", n, limit)
	}
	return -1
}

// checkStorageQuota refuses a server-side write into repo once its
// owner's storage quota is used up, the check sshd makes before a push.
func checkStorageQuota(c *Ctx, repo store.Repo) int {
	return checkBytesLeft(c, repo.OwnerKind, repo.OwnerID, repo.OwnerName, 0)
}

// checkBytesLeft refuses when the owner's storage plus adding exceeds
// its cap (with adding 0, once the cap is used up).
func checkBytesLeft(c *Ctx, kind string, id int64, name string, adding int64) int {
	limit := ByteLimit(c.Store, limitsOf(c), kind, id)
	if limit <= 0 {
		return -1
	}
	used := OwnedBytes(c.Store, c.Cfg.Server.Root, kind, id)
	if adding == 0 && used >= limit {
		return c.fail(protocol.ExitDenied,
			"%s's storage quota is used up (%d of %d bytes); delete something, or ask an admin to raise the limit",
			name, used, limit)
	}
	if adding > 0 && used+adding > limit {
		return c.fail(protocol.ExitDenied,
			"%s's storage quota cannot take %d more bytes (%d of %d used); delete something, or ask an admin to raise the limit",
			name, adding, used, limit)
	}
	return -1
}

// orgCreateMu serialises the org cap check with the insert, as
// repoCreateMu does for repositories.
var orgCreateMu sync.Mutex

func checkOrgQuota(c *Ctx) int {
	limit := OrgLimit(c.Store, limitsOf(c), c.User.ID)
	if limit == 0 {
		return -1
	}
	n, err := c.Store.CreatedOrgCount(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if n >= limit {
		return c.fail(protocol.ExitDenied, "you have created %d of the %d organizations your account may create; delete one, or ask an admin to raise the limit", n, limit)
	}
	return -1
}

func init() {
	register(Command{Path: []string{"admin", "user", "limits"},
		Summary: "show or set an account's repository, storage and organization caps (instance admins)",
		Usage:   "admin user limits <username> [--repos <n>|default] [--bytes <n>|default] [--orgs <n>|default]",
		Flags: []Flag{
			{"--repos", "<n>|default", "the account's repository cap", ""},
			{"--bytes", "<n>|default", "the account's storage cap", ""},
			{"--orgs", "<n>|default", "the account's cap on organizations it creates", ""},
		},
		Examples: []string{"admin user limits alice", "admin user limits alice --repos 50"},
		Run:      runAdminUserLimits})
	register(Command{Path: []string{"admin", "org", "limits"},
		Summary: "show or set an organization's repository and storage caps (instance admins)",
		Usage:   "admin org limits <org> [--repos <n>|default] [--bytes <n>|default]",
		Flags: []Flag{
			{"--repos", "<n>|default", "the organization's repository cap", ""},
			{"--bytes", "<n>|default", "the organization's storage cap", ""},
		},
		Examples: []string{"admin org limits krz", "admin org limits krz --bytes 0"},
		Run:      runAdminOrgLimits})
}

// applyLimitFlags reads --repos/--bytes (and --orgs when orgs is true)
// into l. set reports whether any flag was given.
func applyLimitFlags(c *Ctx, args []string, l *store.Limits, orgs bool) (set bool, code int) {
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return false, c.fail(protocol.ExitUsage, "%s requires a value", args[i])
		}
		v := args[i+1]
		var target **int64
		switch {
		case args[i] == "--repos":
			target = &l.Repos
		case args[i] == "--bytes":
			target = &l.Bytes
		case args[i] == "--orgs" && orgs:
			target = &l.Orgs
		default:
			return false, c.usage()
		}
		if v == "default" {
			*target = nil
		} else {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return false, c.fail(protocol.ExitUsage, "%s takes a non-negative number or default", args[i])
			}
			*target = &n
		}
		set = true
		i++
	}
	return set, -1
}

func capText(n int64) string {
	if n == 0 {
		return "unlimited"
	}
	return strconv.FormatInt(n, 10)
}

func runAdminUserLimits(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) < 1 {
		return c.usage()
	}
	u, err := c.Store.UserByUsername(args[0])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no user %q", args[0])
	}
	l, err := c.Store.OwnerLimits("user", u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	set, code := applyLimitFlags(c, args[1:], &l, true)
	if code >= 0 {
		return code
	}
	if set {
		if err := c.Store.SetOwnerLimits("user", u.ID, l); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		c.Store.Audit(c.User.ID, "admin user.limits", map[string]any{"user": u.Username, "repos": l.Repos, "bytes": l.Bytes, "orgs": l.Orgs})
	}
	type out struct {
		User        string `json:"user"`
		Repos       int64  `json:"repos"` // effective cap, 0 unlimited
		Bytes       int64  `json:"bytes"` // effective cap, 0 unlimited
		Orgs        int64  `json:"orgs"`  // effective cap, 0 unlimited
		ReposOwned  int64  `json:"repos_owned"`
		BytesOwned  int64  `json:"bytes_owned"`
		OrgsCreated int64  `json:"orgs_created"`
		Override    bool   `json:"override"` // any per-account value set
	}
	d := out{User: u.Username, Repos: RepoLimit(c.Store, limitsOf(c), "user", u.ID), Bytes: ByteLimit(c.Store, limitsOf(c), "user", u.ID),
		Orgs: OrgLimit(c.Store, limitsOf(c), u.ID), Override: l.Repos != nil || l.Bytes != nil || l.Orgs != nil}
	d.ReposOwned, _ = c.Store.OwnedRepoCount("user", u.ID)
	d.BytesOwned = OwnedBytes(c.Store, c.Cfg.Server.Root, "user", u.ID)
	d.OrgsCreated, _ = c.Store.CreatedOrgCount(u.ID)
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\trepos %d of %s\tbytes %d of %s\torgs %d of %s\n", d.User,
			d.ReposOwned, capText(d.Repos), d.BytesOwned, capText(d.Bytes), d.OrgsCreated, capText(d.Orgs))
	})
}

func runAdminOrgLimits(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) < 1 {
		return c.usage()
	}
	org, err := c.Store.OrgByName(args[0])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no organization %q", args[0])
	}
	l, err := c.Store.OwnerLimits("org", org.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	set, code := applyLimitFlags(c, args[1:], &l, false)
	if code >= 0 {
		return code
	}
	if set {
		if err := c.Store.SetOwnerLimits("org", org.ID, l); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		c.Store.Audit(c.User.ID, "admin org.limits", map[string]any{"org": org.Name, "repos": l.Repos, "bytes": l.Bytes})
	}
	type out struct {
		Org        string `json:"org"`
		Repos      int64  `json:"repos"` // effective cap, 0 unlimited
		Bytes      int64  `json:"bytes"` // effective cap, 0 unlimited
		ReposOwned int64  `json:"repos_owned"`
		BytesOwned int64  `json:"bytes_owned"`
		Override   bool   `json:"override"` // any per-org value set
	}
	d := out{Org: org.Name, Repos: RepoLimit(c.Store, limitsOf(c), "org", org.ID), Bytes: ByteLimit(c.Store, limitsOf(c), "org", org.ID),
		Override: l.Repos != nil || l.Bytes != nil}
	d.ReposOwned, _ = c.Store.OwnedRepoCount("org", org.ID)
	d.BytesOwned = OwnedBytes(c.Store, c.Cfg.Server.Root, "org", org.ID)
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\trepos %d of %s\tbytes %d of %s\n", d.Org, d.ReposOwned, capText(d.Repos), d.BytesOwned, capText(d.Bytes))
	})
}
