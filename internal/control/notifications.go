package control

import (
	"fmt"
	"io"
	"strconv"
	"strings"

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
	register(Command{Path: []string{"repo", "watch"},
		Summary: "hear about all activity on a repository",
		Usage:   "repo watch <owner/name>", Run: runRepoWatch})
	register(Command{Path: []string{"repo", "unwatch"},
		Summary: "mute a repository, including work you are part of",
		Usage:   "repo unwatch <owner/name>", Run: runRepoUnwatch})
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
}

// notify delivers a notice to the given user ids widened by the
// repository's watchers, minus anyone who muted it and minus the acting
// user. A best-effort side channel: failures are ignored, the action
// itself already succeeded.
func notify(c *Ctx, userIDs []int64, n notice) {
	recipients, err := c.Store.NotifyRecipients(n.repo.ID, c.User.ID, userIDs)
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
		email, err := c.Store.PrimaryVerifiedEmail(id)
		if err != nil || email == "" {
			continue
		}
		c.Store.EnqueueMail(email, n.subject, body)
	}
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

// noticesDefaultLimit caps a bare list; pagination reaches further back.
const noticesDefaultLimit = 50

func runNotificationsList(c *Ctx, args []string) int {
	const usage = "notifications list [--all] [--limit <n>] [--cursor <c>]"
	rest, p, code := parsePageFlags(c, args, "notifications", true)
	if code >= 0 {
		return code
	}
	fl, err := parseFlags(rest, flagSpec{Bools: []string{"--all"}, Usage: usage})
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
	const usage = "notifications read <id>... | --all"
	fl, err := parseFlags(args, flagSpec{Bools: []string{"--all"}, MaxPos: -1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	// --all and a list of ids are two ways of saying which rows: taking
	// both would leave which one won unstated.
	if fl.Has("--all") == (len(fl.Pos) > 0) {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
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

func runRepoWatch(c *Ctx, args []string) int   { return setWatch(c, args, "watching") }
func runRepoUnwatch(c *Ctx, args []string) int { return setWatch(c, args, "muted") }

func setWatch(c *Ctx, args []string, state string) int {
	verb := "watch"
	if state == "muted" {
		verb = "unwatch"
	}
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo %s <owner/name>", verb)
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	if err := c.Store.SetRepoWatch(repo.ID, c.User.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"repo": repo.Path(), "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", state, repo.Path())
	})
}
