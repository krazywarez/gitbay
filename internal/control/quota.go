package control

import (
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Quotas cap what one account owns directly. The limit is the account's
// override when set, else the configured default; 0 is unlimited.

// RepoLimit is the account's repository cap, 0 for none.
func RepoLimit(st *store.Store, cfg configLimits, userID int64) int64 {
	if l, err := st.UserLimits(userID); err == nil && l.Repos != nil {
		return *l.Repos
	}
	return int64(cfg.MaxReposPerUser)
}

// ByteLimit is the account's storage cap in bytes, 0 for none.
func ByteLimit(st *store.Store, cfg configLimits, userID int64) int64 {
	if l, err := st.UserLimits(userID); err == nil && l.Bytes != nil {
		return *l.Bytes
	}
	return cfg.MaxBytesPerUser
}

// OwnedBytes is the disk taken by the repositories a user owns directly.
func OwnedBytes(st *store.Store, root string, userID int64) int64 {
	repos, err := st.ListReposForOwner("user", userID)
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
}

// QuotaConfig is what sshd passes: the limits section of the config.
func QuotaConfig(cfg config.Config) configLimits {
	return configLimits{cfg.Limits.MaxReposPerUser, cfg.Limits.MaxBytesPerUser}
}

func limitsOf(c *Ctx) configLimits {
	return configLimits{c.Cfg.Limits.MaxReposPerUser, c.Cfg.Limits.MaxBytesPerUser}
}

// checkRepoQuota refuses a new user-owned repository past the cap.
func checkRepoQuota(c *Ctx) int {
	limit := RepoLimit(c.Store, limitsOf(c), c.User.ID)
	if limit == 0 {
		return -1
	}
	n, err := c.Store.OwnedRepoCount(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if n >= limit {
		return c.fail(protocol.ExitDenied, "you own %d of the %d repositories your account may hold; delete or transfer one, or ask an admin to raise the limit", n, limit)
	}
	return -1
}

func init() {
	register(Command{Path: []string{"admin", "user", "limits"},
		Summary: "show or set an account's repository and storage caps (instance admins)",
		Usage:   "admin user limits <username> [--repos <n>|default] [--bytes <n>|default]",
		SSHOnly: true, Run: runAdminUserLimits})
}

func runAdminUserLimits(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) < 1 {
		return c.fail(protocol.ExitUsage, "usage: admin user limits <username> [--repos <n>|default] [--bytes <n>|default]")
	}
	u, err := c.Store.UserByUsername(args[0])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no user %q", args[0])
	}
	l, err := c.Store.UserLimits(u.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	set := false
	for i := 1; i < len(args); i++ {
		if i+1 >= len(args) {
			return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
		}
		v := args[i+1]
		var target **int64
		switch args[i] {
		case "--repos":
			target = &l.Repos
		case "--bytes":
			target = &l.Bytes
		default:
			return c.fail(protocol.ExitUsage, "usage: admin user limits <username> [--repos <n>|default] [--bytes <n>|default]")
		}
		if v == "default" {
			*target = nil
		} else {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return c.fail(protocol.ExitUsage, "%s takes a non-negative number or default", args[i])
			}
			*target = &n
		}
		set = true
		i++
	}
	if set {
		if err := c.Store.SetUserLimits(u.ID, l); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		c.Store.Audit(c.User.ID, "admin user.limits", map[string]any{"user": u.Username, "repos": l.Repos, "bytes": l.Bytes})
	}
	type out struct {
		User       string `json:"user"`
		Repos      int64  `json:"repos"` // effective cap, 0 unlimited
		Bytes      int64  `json:"bytes"` // effective cap, 0 unlimited
		ReposOwned int64  `json:"repos_owned"`
		BytesOwned int64  `json:"bytes_owned"`
		Override   bool   `json:"override"` // any per-account value set
	}
	d := out{User: u.Username, Repos: RepoLimit(c.Store, limitsOf(c), u.ID), Bytes: ByteLimit(c.Store, limitsOf(c), u.ID),
		Override: l.Repos != nil || l.Bytes != nil}
	d.ReposOwned, _ = c.Store.OwnedRepoCount(u.ID)
	d.BytesOwned = OwnedBytes(c.Store, c.Cfg.Server.Root, u.ID)
	return c.emit(d, func(w io.Writer) {
		cap := func(n int64) string {
			if n == 0 {
				return "unlimited"
			}
			return strconv.FormatInt(n, 10)
		}
		fmt.Fprintf(w, "%s\trepos %d of %s\tbytes %d of %s\n", d.User, d.ReposOwned, cap(d.Repos), d.BytesOwned, cap(d.Bytes))
	})
}
