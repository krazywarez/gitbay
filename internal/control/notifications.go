package control

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/autolink"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"notifications", "list"},
		Summary:  "your notification inbox, newest first",
		Usage:    "notifications list [--all] [--limit <n>] [--cursor <c>]",
		ReadOnly: true, Run: runNotificationsList})
	register(Command{Path: []string{"notifications", "read"},
		Summary: "mark notifications read",
		Usage:   "notifications read <id>... | --all", Run: runNotificationsRead})
	register(Command{Path: []string{"notifications", "settings", "show"},
		Summary:  "your notification preferences",
		Usage:    "notifications settings show",
		ReadOnly: true, Run: runNotificationsSettingsShow})
	register(Command{Path: []string{"notifications", "settings", "mail"},
		Summary: "activity by mail as well as the inbox (login links are unaffected)",
		Usage:   "notifications settings mail on|off", Run: runNotificationsSettingsMail})
	register(Command{Path: []string{"notifications", "settings", "watch"},
		Summary: "every issue and merge request on repositories you can write to",
		Usage:   "notifications settings watch on|off", Run: runNotificationsSettingsWatch})
	register(Command{Path: []string{"repo", "watch"},
		Summary: "hear about all activity on a repository",
		Usage:   "repo watch <owner/name>", Run: runRepoWatch})
	register(Command{Path: []string{"repo", "unwatch"},
		Summary: "back to the default: only work you are part of",
		Usage:   "repo unwatch <owner/name>", Run: runRepoUnwatch})
	register(Command{Path: []string{"repo", "mute"},
		Summary: "mute a repository, including work you are part of",
		Usage:   "repo mute <owner/name>", Run: runRepoMute})
}

// notice is one thing that happened, in the shape both delivery routes
// need: a mail subject and body, and an inbox row. The inbox is filed
// whether or not the instance has SMTP; mail is the optional half.
type notice struct {
	repo    store.Repo
	kind    string // issue, mr, or build
	subject string // mail subject
	action  string // "opened issue #12" — also the inbox summary
	excerpt string // quoted into the mail, not the inbox
	path    string // web path, no leading slash
	// body replaces the composed mail body outright, for a notice whose
	// mail is not prose — a failed build's log tail is not an excerpt of
	// something someone wrote, and is not cut to an excerpt's length.
	body string
	// direct keeps the notice to the given accounts: watchers of the
	// repository are not added. A mention is addressed to someone.
	direct bool
}

// notify delivers a notice to the given user ids widened by the
// repository's watchers (unless direct), minus anyone who muted it and
// minus the acting user. A best-effort side channel: failures are
// ignored, the action itself already succeeded.
func notify(c *Ctx, userIDs []int64, n notice) {
	recipients, err := c.Store.NotifyRecipients(n.repo.ID, c.User.ID, userIDs, !n.direct)
	if err != nil {
		return
	}
	sendMail := c.Cfg.Mail.SMTPHost != ""
	body := noticeBody(c, n)
	for _, id := range recipients {
		c.Store.AddNotice(id, n.repo.ID, n.kind, c.User.Username, n.action, n.path)
		if !sendMail {
			continue
		}
		email, err := c.Store.ActivityMailAddress(id)
		if err != nil || email == "" {
			continue
		}
		c.Store.EnqueueMail(email, n.subject, body)
	}
}

// notifyMentions files an inbox row for every account text mentions by
// @name that can read the repository, and records them as participants
// of the thread so they hear what follows (#202). Mute is honoured by
// notify; the actor mentioning themselves is dropped there too.
func notifyMentions(c *Ctx, repo store.Repo, t thread, itemID, number int64, title, text string) {
	var ids []int64
	for _, name := range autolink.Mentions(text) {
		u, err := c.Store.UserByUsername(name)
		if err != nil {
			trimmed := strings.TrimRight(name, "._-")
			if trimmed == "" || trimmed == name {
				continue
			}
			if u, err = c.Store.UserByUsername(trimmed); err != nil {
				continue
			}
		}
		if u.ID == c.User.ID {
			continue
		}
		grant, err := c.Store.AccessRole(repo.ID, u.ID)
		if err != nil || !policy.CanRead(u, repo, grant) {
			continue
		}
		ids = append(ids, u.ID)
	}
	if len(ids) == 0 {
		return
	}
	c.Store.AddMentions(repo.ID, t.kind, itemID, ids)
	notify(c, ids, notice{repo: repo, kind: t.kind, direct: true,
		subject: fmt.Sprintf("[%s] %s%d: %s", repo.Path(), t.symbol, number, title),
		action:  fmt.Sprintf("mentioned you in %s%d", t.symbol, number),
		excerpt: text, path: fmt.Sprintf("%s/%s/%d", repo.Path(), t.segment, number)})
}

// noticeBody builds the standard mail body: who did what, an excerpt, and
// the web link.
func noticeBody(c *Ctx, n notice) string {
	if n.body != "" {
		return n.body
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", c.User.Username, n.action)
	if e := strings.TrimSpace(n.excerpt); e != "" {
		if len(e) > 500 {
			e = e[:500] + "…"
		}
		fmt.Fprintf(&b, "\n%s\n", e)
	}
	fmt.Fprintf(&b, "\n%s/%s\n", strings.TrimSuffix(c.Cfg.Server.SiteURL, "/"), n.path)
	return b.String()
}

func issueSubject(repo store.Repo, number int64, title string) string {
	return fmt.Sprintf("[%s] #%d: %s", repo.Path(), number, title)
}

func mrSubject(repo store.Repo, number int64, title string) string {
	return fmt.Sprintf("[%s] !%d: %s", repo.Path(), number, title)
}

func emitNotificationSettings(c *Ctx) int {
	mail, err := c.Store.MailEnabled(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	watch, err := c.Store.WatchEnabled(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]bool{"mail": mail, "watch": watch}, func(w io.Writer) {
		onOff := func(on bool) string {
			if on {
				return "on"
			}
			return "off"
		}
		fmt.Fprintf(w, "mail: %s\nwatch: %s\n", onOff(mail), onOff(watch))
	})
}

func runNotificationsSettingsShow(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	return emitNotificationSettings(c)
}

// runNotificationsSettingsMail is the "inbox but no mail" switch: the
// inbox is filed either way, the mail half consults it (#194).
func runNotificationsSettingsMail(c *Ctx, args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return c.usage()
	}
	if err := c.Store.SetMailEnabled(c.User.ID, args[0] == "on"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitNotificationSettings(c)
}

// runNotificationsSettingsWatch is the default watch state for
// repositories the account can write to: consulted when a notice is
// delivered, so a grant or a revoke needs no watch row of its own (#194).
func runNotificationsSettingsWatch(c *Ctx, args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return c.usage()
	}
	if err := c.Store.SetWatchEnabled(c.User.ID, args[0] == "on"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitNotificationSettings(c)
}

// noticesDefaultLimit caps a bare list; pagination reaches further back.
const noticesDefaultLimit = 50

func runNotificationsList(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "notifications", true)
	if code >= 0 {
		return code
	}
	fl, err := parseFlags(rest, flagSpec{Bools: []string{"--all"}, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	all := fl.Has("--all")
	if p.limit == 0 {
		p.limit = noticesDefaultLimit
	}
	notices, err := c.Store.Inbox(c.User.ID, !all, p.queryLimit(), p.keyInt())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		ID        int64  `json:"id"`
		Repo      string `json:"repo"`
		Kind      string `json:"kind"`
		Actor     string `json:"actor"`
		Summary   string `json:"summary"`
		Path      string `json:"path"`
		CreatedAt string `json:"created_at"`
		ReadAt    string `json:"read_at,omitempty"`
	}
	notices, next := trimPage(p, notices, "notifications", func(n store.Notice) string {
		return strconv.FormatInt(n.ID, 10)
	})
	ds := make([]out, 0, len(notices))
	for _, n := range notices {
		ds = append(ds, out{n.ID, n.RepoPath, n.Kind, n.Actor, n.Summary, n.Path, n.CreatedAt, n.ReadAt})
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			mark := "*"
			if d.ReadAt != "" {
				mark = " "
			}
			fmt.Fprintf(w, "%s %d\t%s\t%s\t%s %s\t%s\n",
				mark, d.ID, d.CreatedAt, d.Repo, d.Actor, d.Summary, d.Path)
		}
	})
}

func runNotificationsRead(c *Ctx, args []string) int {
	fl, err := parseFlags(args, flagSpec{Bools: []string{"--all"}, MaxPos: -1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	// --all and a list of ids are two ways of saying which rows: taking
	// both would leave which one won unstated.
	if fl.Has("--all") == (len(fl.Pos) > 0) {
		return c.usage()
	}
	var ids []int64
	for _, a := range fl.Pos {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return c.fail(protocol.ExitUsage, "bad notification id %q", a)
		}
		ids = append(ids, n)
	}
	n, err := c.Store.MarkNoticesRead(c.User.ID, ids)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]int64{"read": n}, func(w io.Writer) {
		fmt.Fprintf(w, "marked %d read\n", n)
	})
}

func runRepoWatch(c *Ctx, args []string) int   { return setWatch(c, args, "watch", "watching") }
func runRepoMute(c *Ctx, args []string) int    { return setWatch(c, args, "mute", "muted") }
func runRepoUnwatch(c *Ctx, args []string) int { return setWatch(c, args, "unwatch", "default") }

// setWatch records the caller's state on a repository. "default" deletes
// the row: watch then unwatch leaves no trace, and a mute is undone the
// same way.
func setWatch(c *Ctx, args []string, verb, state string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	var err error
	if state == "default" {
		err = c.Store.ClearRepoWatch(repo.ID, c.User.ID)
	} else {
		err = c.Store.SetRepoWatch(repo.ID, c.User.ID, state)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"repo": repo.Path(), "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", state, repo.Path())
	})
}
