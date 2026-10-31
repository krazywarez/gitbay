// Package botissue keeps the issues gitbay-bot maintains for a repository:
// dependency updates, dependency advisories, code scanning findings. Each
// is one issue per repository, opened when something is first reported,
// rewritten when the set changes, and closed when nothing is left.
package botissue

import (
	"fmt"
	"strings"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// Tracked is one issue and the set it reports.
type Tracked struct {
	Title   string
	Number  int64 // the issue last opened, 0 for none
	Record  func(number int64) error
	Empty   bool // nothing to report
	Changed bool // the set differs from the last report
	Save    func() error
	Body    func() string
}

// Maintain brings the issue in line with the set.
func Maintain(st *store.Store, cfg config.Config, repo store.Repo, t Tracked) error {
	issue, hasIssue := openIssue(st, repo, t.Number)
	if t.Empty {
		if hasIssue {
			st.SetIssueState(issue.ID, "closed")
			t.Record(0)
		}
		if t.Changed {
			return t.Save()
		}
		return nil
	}
	// Nothing changed since the last report: leave it be, whether the issue
	// is still open or the maintainer has closed it. Reopening on an
	// unchanged set would make closing the issue pointless.
	if !t.Changed {
		return nil
	}
	if err := t.Save(); err != nil {
		return err
	}
	body := t.Body()
	if hasIssue {
		if err := st.UpdateIssueText(issue.ID, nil, &body, nil); err != nil {
			return err
		}
		notify(st, cfg, repo, t.Title, issue.Number, fmt.Sprintf("updated issue #%d", issue.Number), body)
		return nil
	}
	author, err := st.UserByUsername(store.BotUsername)
	if err != nil {
		// Migration 0028 leaves the account uncreated when the name was
		// already taken, which is the one case worth spelling out: the
		// feature is stuck until an operator frees the name.
		return fmt.Errorf("no %s account to author the issue (the name was taken when this instance upgraded): %w",
			store.BotUsername, err)
	}
	number, err := st.CreateIssue(repo.ID, author.ID, t.Title, body, "md")
	if err != nil {
		return err
	}
	if err := t.Record(number); err != nil {
		return err
	}
	st.RecordEvent(repo.ID, author.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, number))
	notify(st, cfg, repo, t.Title, number, fmt.Sprintf("opened issue #%d", number), body)
	return nil
}

// openIssue loads the issue being maintained, if there still is one. A
// closed issue counts as gone: reopening one the maintainer closed would be
// arguing with them, so the next change opens a fresh issue.
func openIssue(st *store.Store, repo store.Repo, number int64) (store.Issue, bool) {
	if number == 0 {
		return store.Issue{}, false
	}
	issue, err := st.IssueByNumber(repo.ID, number)
	if err != nil || issue.State != "open" {
		return store.Issue{}, false
	}
	return issue, true
}

// notify files an inbox row for the repo's owners and watchers and mails
// them, the same targets and shape as an issue opened over SSH.
// Best-effort, like every other notification.
func notify(st *store.Store, cfg config.Config, repo store.Repo, title string, number int64, action, body string) {
	targets, err := st.RepoNotifyTargets(repo)
	if err != nil {
		return
	}
	author, err := st.UserByUsername(store.BotUsername)
	if err != nil {
		return
	}
	recipients, err := st.NotifyRecipients(repo.ID, author.ID, targets, true)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("[%s] #%d: %s", repo.Path(), number, title)
	text := fmt.Sprintf("%s %s\n\n%s\n%s/%s/issues/%d\n", store.BotUsername, action, body,
		strings.TrimSuffix(cfg.Server.SiteURL, "/"), repo.Path(), number)
	path := fmt.Sprintf("%s/issues/%d", repo.Path(), number)
	for _, id := range recipients {
		st.AddNotice(id, repo.ID, "issue", store.BotUsername, action, path)
		if cfg.Mail.SMTPHost == "" {
			continue
		}
		email, err := st.ActivityMailAddress(id)
		if err != nil || email == "" {
			continue
		}
		st.EnqueueMail(email, subject, text)
	}
}
