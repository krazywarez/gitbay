package control

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/backuplock"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// DeletionGrace is how long a confirmed deletion waits before the purge.
// Signing in during it cancels.
const DeletionGrace = 7 * 24 * time.Hour

// deletionLinkTTL is how long the mailed confirmation link works.
const deletionLinkTTL = 24 * time.Hour

func init() {
	register(Command{Path: []string{"account", "delete"},
		NeedsRecentSignIn: true,
		Summary:           "delete your account: mails a link, then purges seven days after it is opened",
		Usage:             "account delete --confirm <username> | --cancel",
		Flags: []Flag{
			{"--confirm", "<username>", "your username, typed out", ""},
			{"--cancel", "", "withdraw a request that has not been confirmed", ""},
		},
		Examples: []string{"account delete --confirm alice"},
		Run:      runAccountDelete})
}

func runAccountDelete(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--confirm"}, Bools: []string{"--cancel"},
		Usage: "account delete --confirm <username> | --cancel"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if f.Has("--cancel") {
		had, err := c.Store.CancelAccountDeletion(c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !had {
			return c.fail(protocol.ExitNotFound, "no deletion is pending for %s", c.User.Username)
		}
		c.Store.Audit(c.User.ID, "account.delete.cancelled", map[string]any{"user": c.User.Username})
		return c.emit(map[string]any{"user": c.User.Username, "cancelled": true}, func(w io.Writer) {
			fmt.Fprintf(w, "deletion of %s cancelled\n", c.User.Username)
		})
	}
	if f.Value("--confirm") != c.User.Username {
		return c.fail(protocol.ExitUsage, "type your username to confirm: account delete --confirm %s", c.User.Username)
	}
	if c.User.IsAdmin {
		if n, err := c.Store.OtherActiveAdmins(c.User.ID); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		} else if n == 0 {
			return c.fail(protocol.ExitDenied, "you are the instance's only admin; promote another account first")
		}
	}
	orgs, err := c.Store.SoleAdminOrgs(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if len(orgs) > 0 {
		return c.fail(protocol.ExitDenied, "you are the only admin of %s; add another admin or delete the organization first",
			strings.Join(orgs, ", "))
	}
	address, err := c.Store.PreferredVerifiedEmail(c.User.ID)
	if err != nil || address == "" {
		return c.fail(protocol.ExitDenied, "deletion is confirmed by mail; add and verify an address first (email add)")
	}
	token, hash, err := store.NewToken()
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.RequestAccountDeletion(c.User.ID, hash, deletionLinkTTL); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	host := siteHost(c.Cfg)
	body := fmt.Sprintf(
		"Someone (hopefully you) asked to delete the account %s on %s.\n\n"+
			"To go ahead, open this link within 24 hours:\n\n    %s/settings/delete?token=%s\n\n"+
			"Confirming disables the account at once. Seven days later it is deleted:\n"+
			"its repositories, snippets, keys and addresses go, and what it wrote on\n"+
			"other people's repositories stays under the name \"ghost\".\n\n"+
			"Signing in during those seven days, on the web or over SSH with a\n"+
			"full-scope key, cancels the deletion.\n\n"+
			"To keep a copy first: ssh git@%s account export > bundle.json\n\n"+
			"If this wasn't you, ignore this mail. Nothing has changed on the account.\n",
		c.User.Username, host, strings.TrimSuffix(c.Cfg.Server.SiteURL, "/"), token, host)
	if err := c.Store.EnqueueMail(address, "delete your account on "+host, body); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "account.delete.requested", map[string]any{"user": c.User.Username})
	return c.emit(map[string]any{"user": c.User.Username, "mailed": address}, func(w io.Writer) {
		fmt.Fprintf(w, "mailed a confirmation link to %s; it works for 24 hours\n", address)
		fmt.Fprintf(w, "nothing changes until it is opened. keep a copy first: account export > bundle.json\n")
	})
}

// ScheduledRefusal is what a credential that cannot cancel a scheduled
// deletion is told.
func ScheduledRefusal(u store.User) string {
	if u.DeleteAfter == store.Purging {
		return "this account is being deleted"
	}
	return fmt.Sprintf("this account is scheduled for deletion at %s; sign in on the web or over SSH with a full-scope key to cancel", u.DeleteAfter)
}

// CancelScheduledDeletion is what signing in does to an account scheduled
// for deletion: the schedule goes and the account is enabled. It reports
// whether there was one.
func CancelScheduledDeletion(st *store.Store, u *store.User, how string) bool {
	if u.DeleteAfter == "" {
		return false
	}
	if had, err := st.CancelAccountDeletion(u.ID); err != nil || !had {
		return false
	}
	st.Audit(u.ID, "account.delete.cancelled", map[string]any{"user": u.Username, "by": how})
	u.Disabled, u.DeleteAfter = false, ""
	return true
}

// PurgeDueAccounts deletes every account whose grace period has passed.
// An account that became the only admin of an org since it was scheduled
// is skipped and audited; an instance admin resolves it. One account's
// failure does not hold up the others; it is retried on the next tick.
func PurgeDueAccounts(cfg config.Config, st *store.Store, now time.Time) ([]string, error) {
	due, err := st.DueDeletions(now)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	var purged []string
	var errs []error
	for _, u := range due {
		if u.DeleteAfter != store.Purging {
			if orgs, err := st.SoleAdminOrgs(u.ID); err != nil {
				errs = append(errs, err)
				continue
			} else if len(orgs) > 0 {
				st.Audit(0, "account.delete.blocked", map[string]any{"user": u.Username, "orgs": orgs})
				continue
			}
		}
		// The claim is what a cancel races: once it holds, signing in
		// no longer cancels, and before it the purge has touched nothing.
		if ok, err := st.ClaimDeletion(u.ID, now); err != nil {
			errs = append(errs, err)
			continue
		} else if !ok {
			continue
		}
		if err := purgeAccount(cfg, st, u); err != nil {
			errs = append(errs, fmt.Errorf("purging %s: %w", u.Username, err))
			continue
		}
		st.Audit(0, "account.delete.purged", map[string]any{"user": u.Username})
		purged = append(purged, u.Username)
	}
	return purged, errors.Join(errs...)
}

func purgeAccount(cfg config.Config, st *store.Store, u store.User) error {
	ghost, err := st.EnsureGhost()
	if err != nil {
		return err
	}
	repos, err := st.ListReposForOwner("user", u.ID)
	if err != nil {
		return err
	}
	if len(repos) > 0 {
		release, err := backuplock.TryShared(cfg.Server.Root)
		if err != nil {
			return err
		}
		for _, r := range repos {
			if err := removeRepo(st, cfg.Server.Root, r); err != nil {
				release()
				return err
			}
		}
		release()
	}
	if err := st.ReassignToGhost(u.ID, ghost); err != nil {
		return err
	}
	return st.DeleteUser(u.ID)
}

// errGhost refuses an admin action on the ghost account.
var errGhost = errors.New("ghost stands in for deleted accounts and cannot be changed")
