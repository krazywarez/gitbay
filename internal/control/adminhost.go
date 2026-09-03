package control

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// The account, email, invite and stats commands gitbayd admin used to
// implement on its own. They live here so the host binary and an admin
// session run the same code; gitbayd admin dispatches into these.

func init() {
	register(Command{Path: []string{"admin", "user", "create"},
		Summary:    "create an account, optionally with a key and a verified address (instance admins)",
		Usage:      "admin user create <username> [--admin] [--email <address> [--verified]] [--key -] < key.pub",
		ReadsStdin: true, SSHOnly: true, Run: runAdminUserCreate})
	register(Command{Path: []string{"admin", "user", "disable"},
		Summary: "suspend an account: SSH, web sessions and API tokens refused until re-enabled",
		Usage:   "admin user disable <username>",
		SSHOnly: true, Run: runAdminUserDisable})
	register(Command{Path: []string{"admin", "user", "enable"},
		Summary: "restore a suspended account",
		Usage:   "admin user enable <username>",
		SSHOnly: true, Run: runAdminUserEnable})
	register(Command{Path: []string{"admin", "user", "delete"},
		Summary: "delete an account that anchors nothing (keys, emails and sessions go with it)",
		Usage:   "admin user delete <username> --yes",
		SSHOnly: true, Run: runAdminUserDelete})
	register(Command{Path: []string{"admin", "email", "verify"},
		Summary: "mark an address verified by admin assertion",
		Usage:   "admin email verify <username> <address>",
		SSHOnly: true, Run: runAdminEmailVerify})
	register(Command{Path: []string{"admin", "invite"},
		Summary: "issue a registration invite and mail its code",
		Usage:   "admin invite --email <address>",
		SSHOnly: true, Run: runAdminInvite})
	register(Command{Path: []string{"admin", "stats"},
		Summary:  "instance statistics: counts and per-repository disk usage",
		Usage:    "admin stats",
		ReadOnly: true, SSHOnly: true, Run: runAdminStats})
}

func runAdminUserCreate(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	const usage = "usage: admin user create <username> [--admin] [--email <address> [--verified]] [--key -] < key.pub"
	var username, email string
	var isAdmin, verified, withKey bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--admin":
			isAdmin = true
		case "--verified":
			verified = true
		case "--email":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--email requires a value")
			}
			email = args[i+1]
			i++
		case "--key":
			if i+1 >= len(args) || args[i+1] != "-" {
				return c.fail(protocol.ExitUsage, "--key only supports - (the public key on stdin)")
			}
			withKey = true
			i++
		default:
			if username != "" || len(args[i]) == 0 || args[i][0] == '-' {
				return c.fail(protocol.ExitUsage, usage)
			}
			username = args[i]
		}
	}
	if username == "" || (verified && email == "") {
		return c.fail(protocol.ExitUsage, usage)
	}
	if err := policy.ValidateOwnerName(username); err != nil {
		return c.failErr(err)
	}
	// Parse the key before creating anything, so a bad key leaves no
	// half-made account behind.
	var pub ssh.PublicKey
	if withKey {
		raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
		if err != nil {
			return c.fail(protocol.ExitFailure, "reading key: %v", err)
		}
		if pub, _, _, _, err = ssh.ParseAuthorizedKey(raw); err != nil {
			return c.fail(protocol.ExitUsage, "not a public key in authorized_keys format: %v", err)
		}
	}
	uid, err := c.Store.CreateUser(username, isAdmin)
	if err != nil {
		return c.failErr(err)
	}
	if email != "" {
		by := ""
		if verified {
			by = "admin"
		}
		if err := c.Store.AddEmail(uid, email, by, true); err != nil {
			return c.failErr(err)
		}
	}
	fp := ""
	if pub != nil {
		fp = ssh.FingerprintSHA256(pub)
		if err := c.Store.AddSSHKey(uid, fp, pub.Type(), pub.Marshal(), "full"); err != nil {
			return c.failErr(err)
		}
	}
	c.Store.Audit(c.User.ID, "admin user.created", map[string]any{"user": username})
	type out struct {
		User        string `json:"user"`
		Admin       bool   `json:"admin,omitempty"`
		Fingerprint string `json:"fingerprint,omitempty"`
	}
	return c.emit(out{username, isAdmin, fp}, func(w io.Writer) {
		if fp != "" {
			fmt.Fprintln(w, "key", fp)
		}
		fmt.Fprintln(w, "created user", username)
	})
}

// adminUserArg resolves the single username argument of an admin command.
func adminUserArg(c *Ctx, args []string, usage string) (store.User, int) {
	if code := requireInstanceAdmin(c); code >= 0 {
		return store.User{}, code
	}
	if len(args) != 1 {
		return store.User{}, c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	u, err := c.Store.UserByUsername(args[0])
	if errors.Is(err, store.ErrNotFound) {
		return u, c.fail(protocol.ExitNotFound, "no user %q", args[0])
	} else if err != nil {
		return u, c.fail(protocol.ExitFailure, "%v", err)
	}
	return u, -1
}

func runAdminUserDisable(c *Ctx, args []string) int {
	u, code := adminUserArg(c, args, "admin user disable <username>")
	if code >= 0 {
		return code
	}
	if err := c.Store.SetUserDisabled(u.ID, true); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "admin user.disabled", map[string]any{"user": u.Username})
	return c.emit(map[string]any{"user": u.Username, "disabled": true}, func(w io.Writer) {
		fmt.Fprintf(w, "disabled %s: SSH, web sessions, and API tokens are refused; nothing was deleted\n", u.Username)
	})
}

func runAdminUserEnable(c *Ctx, args []string) int {
	u, code := adminUserArg(c, args, "admin user enable <username>")
	if code >= 0 {
		return code
	}
	if err := c.Store.SetUserDisabled(u.ID, false); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "admin user.enabled", map[string]any{"user": u.Username})
	return c.emit(map[string]any{"user": u.Username, "disabled": false}, func(w io.Writer) {
		fmt.Fprintf(w, "enabled %s\n", u.Username)
	})
}

func runAdminUserDelete(c *Ctx, args []string) int {
	var rest []string
	var yes bool
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else {
			rest = append(rest, a)
		}
	}
	u, code := adminUserArg(c, rest, "admin user delete <username> --yes")
	if code >= 0 {
		return code
	}
	if !yes {
		return c.fail(protocol.ExitUsage, "deletion is permanent; pass --yes")
	}
	if u.ID == c.User.ID {
		return c.fail(protocol.ExitUsage, "that is your own account")
	}
	if err := c.Store.DeleteUser(u.ID); err != nil {
		return c.failErr(err)
	}
	c.Store.Audit(c.User.ID, "admin user.deleted", map[string]any{"user": u.Username})
	return c.emit(map[string]string{"deleted": u.Username}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted %s\n", u.Username)
	})
}

func runAdminEmailVerify(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: admin email verify <username> <address>")
	}
	u, err := c.Store.UserByUsername(args[0])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no user %q", args[0])
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.VerifyEmail(u.ID, args[1], "admin"); err != nil {
		c.Store.Audit(c.User.ID, "admin email.verify_failed", map[string]any{"user": args[0], "email": args[1]})
		return c.fail(protocol.ExitNotFound, "no address %s on user %s", args[1], args[0])
	}
	c.Store.Audit(c.User.ID, "admin email.verified", map[string]any{"user": args[0], "email": args[1]})
	return c.emit(map[string]string{"user": args[0], "verified": args[1]}, func(w io.Writer) {
		fmt.Fprintln(w, "verified", args[1])
	})
}

func runAdminInvite(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	email := ""
	if len(args) == 2 && args[0] == "--email" {
		email = args[1]
	}
	if email == "" {
		return c.fail(protocol.ExitUsage, "usage: admin invite --email <address>")
	}
	if used, err := c.Store.EmailInUse(email); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if used {
		return c.fail(protocol.ExitUsage, "%s already belongs to an account; invites are for new users", email)
	}
	code, hash, err := store.NewToken()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.CreateInvite(hash, email); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	host := siteHost(c.Cfg)
	body := fmt.Sprintf(
		"You have been invited to %s.\n\nCreate your account by running (with the SSH key you want to use):\n\n"+
			"    ssh git@%s register --username <name> --invite %s\n\n"+
			"The invite is single-use and tied to this address.\n", host, host, code)
	type out struct {
		Email  string `json:"email"`
		Mailed bool   `json:"mailed"`
		Code   string `json:"code,omitempty"` // only when it could not be mailed
	}
	if c.Cfg.Mail.SMTPHost != "" {
		if err := mail.Send(c.Cfg, email, "your invite to "+host, body); err != nil {
			return c.fail(protocol.ExitFailure, "invite stored but mail failed: %v (code: %s)", err, code)
		}
		c.Store.Audit(c.User.ID, "admin invite.issued", map[string]any{"email": email})
		return c.emit(out{Email: email, Mailed: true}, func(w io.Writer) {
			fmt.Fprintf(w, "invite emailed to %s\n", email)
		})
	}
	return c.emit(out{Email: email, Code: code}, func(w io.Writer) {
		fmt.Fprintf(w, "invite for %s (no SMTP configured; deliver it yourself):\n%s\n", email, code)
	})
}

func runAdminStats(c *Ctx, args []string) int {
	if code := requireInstanceAdmin(c); code >= 0 {
		return code
	}
	if len(args) != 0 {
		return c.fail(protocol.ExitUsage, "usage: admin stats")
	}
	counts, err := c.Store.InstanceCounts()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	repos, err := c.Store.ListAllRepos()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type repoDisk struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	}
	type out struct {
		Counts    store.Counts `json:"counts"`
		DBBytes   int64        `json:"db_bytes"`
		RepoBytes int64        `json:"repo_bytes"`
		LFSBytes  int64        `json:"lfs_bytes"`
		Repos     []repoDisk   `json:"repos"`
	}
	d := out{Counts: counts, Repos: []repoDisk{}}
	d.LFSBytes = lfs.LocalStore{Root: lfs.RootFor(c.Cfg.LFS.Root, c.Cfg.Server.Root)}.Size()
	for _, r := range repos {
		b := gitutil.DirSize(RepoDir(c.Cfg.Server.Root, r.OwnerName, r.Name))
		d.Repos = append(d.Repos, repoDisk{r.Path(), b})
		d.RepoBytes += b
	}
	if fi, err := os.Stat(c.Cfg.Server.Root + "/gitbay.db"); err == nil {
		d.DBBytes = fi.Size()
	}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "users %d · orgs %d · repos %d · issues %d (%d open) · MRs %d (%d open)\n",
			counts.Users, counts.Orgs, counts.Repos,
			counts.Issues, counts.OpenIssues, counts.MRs, counts.OpenMRs)
		fmt.Fprintf(w, "database %s · repositories %s · lfs %s\n\n", humanBytes(d.DBBytes), humanBytes(d.RepoBytes), humanBytes(d.LFSBytes))
		for _, r := range d.Repos {
			fmt.Fprintf(w, "%s\t%s\n", r.Path, humanBytes(r.Bytes))
		}
	})
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
