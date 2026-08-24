package control

import (
	"fmt"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// notifyUsers enqueues activity mail for the given user ids, excluding the
// acting user and anyone without a verified primary email. A best-effort
// side channel: failures are ignored, the action itself already succeeded.
// No-op when the instance has no SMTP.
func notifyUsers(c *Ctx, userIDs []int64, subject, body string) {
	if c.Cfg.Mail.SMTPHost == "" {
		return
	}
	seen := map[int64]bool{c.User.ID: true}
	for _, id := range userIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		email, err := c.Store.PrimaryVerifiedEmail(id)
		if err != nil || email == "" {
			continue
		}
		c.Store.EnqueueMail(email, subject, body)
	}
}

// notifyBody builds the standard notification body: who did what, an
// excerpt, and the web link.
func notifyBody(c *Ctx, action, excerpt, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", c.User.Username, action)
	if e := strings.TrimSpace(excerpt); e != "" {
		if len(e) > 500 {
			e = e[:500] + "…"
		}
		fmt.Fprintf(&b, "\n%s\n", e)
	}
	fmt.Fprintf(&b, "\n%s/%s\n", strings.TrimSuffix(c.Cfg.Server.SiteURL, "/"), path)
	return b.String()
}

func issueSubject(repo store.Repo, number int64, title string) string {
	return fmt.Sprintf("[%s] #%d: %s", repo.Path(), number, title)
}

func mrSubject(repo store.Repo, number int64, title string) string {
	return fmt.Sprintf("[%s] !%d: %s", repo.Path(), number, title)
}
