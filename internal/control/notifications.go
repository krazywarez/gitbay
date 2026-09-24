package control

import (
	"errors"
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
	register(Command{Path: []string{"notifications", "device", "add"},
		Summary: "register an Apple device for push, token on stdin",
		Usage:   "notifications device add [--label <name>] < token",
		// Mandatory: without it control.go swaps in an empty reader and
		// this command stores an empty token without erroring.
		ReadsStdin: true, Run: runNotificationsDeviceAdd})
	register(Command{Path: []string{"notifications", "device", "list"},
		Summary:  "your registered devices",
		Usage:    "notifications device list",
		ReadOnly: true, Run: runNotificationsDeviceList})
	register(Command{Path: []string{"notifications", "device", "remove"},
		Summary: "deregister a device",
		Usage:   "notifications device remove <id>", Run: runNotificationsDeviceRemove})
	register(Command{Path: []string{"notifications", "settings", "push"},
		Summary: "activity on your registered devices as well as the inbox",
		Usage:   "notifications settings push on|off", Run: runNotificationsSettingsPush})
	register(Command{Path: []string{"repo", "watch"},
		Summary:  "hear about all activity on a repository",
		Usage:    "repo watch <owner/name>",
		Examples: []string{"repo watch krz/gitbay"},
		Run:      runRepoWatch})
	register(Command{Path: []string{"repo", "unwatch"},
		Summary:  "back to the default: only work you are part of",
		Usage:    "repo unwatch <owner/name>",
		Examples: []string{"repo unwatch krz/gitbay"},
		Run:      runRepoUnwatch})
	register(Command{Path: []string{"repo", "mute"},
		Summary:  "mute a repository, including work you are part of",
		Usage:    "repo mute <owner/name>",
		Examples: []string{"repo mute krz/gitbay"},
		Run:      runRepoMute})
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
	// Nothing drains push_queue unless the daemon started the deliverer,
	// and the retention sweep only collects rows that were sent or
	// dead-lettered, so a row written here would sit there forever.
	sendPush := c.Cfg.Push.Enabled
	body := noticeBody(c, n)
	for _, id := range recipients {
		c.Store.AddNotice(id, n.repo.ID, n.kind, c.User.Username, n.action, n.path)
		if sendPush {
			c.Store.EnqueuePush(id, pushTitle(n), pushBody(c.User.Username, n), n.path)
		}
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

// pushTitle and pushBody are the alert's two lines. The body is built
// from the same two values AddNotice files, so the alert and the inbox
// row cannot disagree about what happened. The title is the repository,
// which also groups a repository's notices in Notification Center.
func pushTitle(n notice) string { return n.repo.Path() }

func pushBody(actor string, n notice) string { return actor + " " + n.action }

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
	push, err := c.Store.PushEnabled(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]bool{"mail": mail, "watch": watch, "push": push}, func(w io.Writer) {
		onOff := func(on bool) string {
			if on {
				return "on"
			}
			return "off"
		}
		v := c.view(w)
		v.fields(
			"mail", onOff(mail),
			"watch", onOff(watch),
			"push", onOff(push),
		)
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

func runNotificationsSettingsPush(c *Ctx, args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return c.usage()
	}
	if err := c.Store.SetPushEnabled(c.User.ID, args[0] == "on"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitNotificationSettings(c)
}

// maxDeviceTokenBytes is well past APNs' 32-byte token rendered as 64 hex
// characters, and stops a stdin that is not a token from becoming a row.
const maxDeviceTokenBytes = 512

func runNotificationsDeviceAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--label"}, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 0 {
		return c.usage()
	}
	// The registration itself would succeed and then deliver nothing,
	// while notifications settings show still reported push on. Say what
	// is actually wrong instead.
	if !c.Cfg.Push.Enabled {
		return c.fail(protocol.ExitFailure,
			"this instance does not send push notifications ([push] enabled = false); ask an admin")
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, maxDeviceTokenBytes+1))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading stdin: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return c.usageWith("no device token on stdin")
	}
	if len(token) > maxDeviceTokenBytes {
		return c.fail(protocol.ExitUsage, "device token is too long")
	}
	id, err := c.Store.AddPushDevice(c.User.ID, token, f.Value("--label"))
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"id": id, "status": "registered"}, func(w io.Writer) {
		fmt.Fprintf(w, "registered device %d\n", id)
	})
}

func runNotificationsDeviceList(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	devices, err := c.Store.PushDevices(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type row struct {
		ID    int64  `json:"id"`
		Label string `json:"label"`
		Token string `json:"token"` // truncated; a token is not echoed in full
		Added string `json:"added"`
	}
	rows := make([]row, 0, len(devices))
	for _, d := range devices {
		rows = append(rows, row{ID: d.ID, Label: d.Label,
			Token: ShortToken(d.Token), Added: d.CreatedAt})
	}
	return c.emit(rows, func(w io.Writer) {
		tb := c.table(w, "ID", "LABEL", "TOKEN", "ADDED")
		for _, r := range rows {
			tb.row(cRef(fmt.Sprintf("%d", r.ID)), cText(r.Label), cText(r.Token), cAge(r.Added))
		}
		tb.flush()
	})
}

// ShortToken renders a device token as its first eight characters. Enough
// to tell two devices apart in a list, not enough to push to one. A real
// APNs token is 64 hex characters, so anything at or under the cut length
// is not a token worth showing part of — it is masked outright rather
// than echoed whole, which "abc…" would imply is a truncation.
//
// Exported because the account page lists the same devices: one renderer,
// so the two surfaces cannot come to disagree about what they print.
func ShortToken(t string) string {
	if len(t) > 8 {
		return t[:8] + "…"
	}
	return "(short token)"
}

func runNotificationsDeviceRemove(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.usageWith("device id must be a number")
	}
	if err := c.Store.RemovePushDevice(c.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no such device; notifications device list shows yours")
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"status": "removed"}, func(w io.Writer) {
		fmt.Fprintln(w, "device removed")
	})
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
		tb := c.table(w, "ID", "WHEN", "REPO", "EVENT", "PATH")
		for _, d := range ds {
			mark := "*"
			if d.ReadAt != "" {
				mark = " "
			}
			tb.row(cRef(fmt.Sprintf("%s %d", mark, d.ID)), cAge(d.CreatedAt), cRef(d.Repo),
				cFlex(fmt.Sprintf("%s %s", d.Actor, d.Summary)), cText(d.Path))
		}
		tb.flush()
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
