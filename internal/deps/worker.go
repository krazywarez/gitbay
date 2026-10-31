package deps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// manifestLimit bounds one manifest read out of the object store.
const manifestLimit = 1 << 20

// lookups is how many registry requests one repository has in flight.
const lookups = 4

// Worker sweeps repositories that have opted in, comparing their manifests
// against the registries and OSV and maintaining up to two issues per
// repository: one for updates, one for advisories.
type Worker struct {
	St      *store.Store
	Cfg     config.Config
	RepoDir func(owner, name string) string
	Client  *Client
	Tick    time.Duration
}

func New(st *store.Store, cfg config.Config, repoDir func(owner, name string) string, version string) *Worker {
	tick := time.Hour
	if v := os.Getenv("GITBAY_DEPS_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Worker{St: st, Cfg: cfg, RepoDir: repoDir, Client: NewClient(version), Tick: tick}
}

// Run sweeps until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Sweep(ctx)
		}
	}
}

// Sweep checks every repository whose interval has elapsed. Split from the
// ticker for tests.
func (w *Worker) Sweep(ctx context.Context) {
	interval := w.Cfg.Deps.CheckIntervalHours
	if interval <= 0 {
		interval = 24
	}
	due, err := w.St.DueDepChecks(interval * 3600)
	if err != nil {
		slog.Error("deps: listing due repos", "err", err)
		return
	}
	for _, repo := range due {
		if ctx.Err() != nil {
			return
		}
		msg := ""
		if err := w.check(ctx, repo); err != nil {
			slog.Warn("deps check failed", "repo", repo.Path(), "err", err)
			msg = err.Error()
		}
		// Stamped either way, so a repo that fails every time is retried on
		// the interval rather than on every sweep.
		w.St.SetDepCheckResult(repo.ID, msg)
	}
}

// check compares one repository against the registries and OSV, and
// reconciles its two issues with the result. A failure on one side leaves
// the other reconciled, and leaves its own issue as it was rather than
// closing it on an answer that never came.
func (w *Worker) check(ctx context.Context, repo store.Repo) error {
	dir := w.RepoDir(repo.OwnerName, repo.Name)
	sha, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return nil // empty repo, or no default branch yet
	}
	read := func(path string) ([]byte, error) {
		return gitutil.ReadBlob(dir, sha, path, manifestLimit)
	}
	var root []string
	if entries, err := gitutil.ListTree(dir, sha, ""); err == nil {
		for _, e := range entries {
			root = append(root, e.Name)
		}
	}
	return errors.Join(
		w.checkUpdates(ctx, repo, Scan(read)),
		w.checkAdvisories(ctx, repo, Resolved(read, root)),
	)
}

func (w *Worker) checkUpdates(ctx context.Context, repo store.Repo, found []Dep) error {
	if len(found) == 0 {
		return w.reconcile(repo, nil)
	}
	behind, err := w.behind(ctx, found)
	if err != nil {
		return err
	}
	return w.reconcile(repo, behind)
}

func (w *Worker) checkAdvisories(ctx context.Context, repo store.Repo, resolved []Dep) error {
	if len(resolved) == 0 {
		return w.reconcileAdvisories(repo, nil)
	}
	matched, err := w.Client.Advisories(ctx, resolved)
	if err != nil {
		return fmt.Errorf("advisories: %w", err)
	}
	return w.reconcileAdvisories(repo, matched)
}

// behind queries the registries and keeps the dependencies whose latest
// release is greater than what the manifest declares. A lookup that fails
// is dropped rather than failing the sweep: one unreachable package should
// not silence the rest, and the next sweep tries again.
func (w *Worker) behind(ctx context.Context, found []Dep) ([]store.DepReport, error) {
	var (
		mu      sync.Mutex
		out     []store.DepReport
		errs    []string
		wg      sync.WaitGroup
		tickets = make(chan struct{}, lookups)
	)
	for _, d := range found {
		wg.Add(1)
		go func(d Dep) {
			defer wg.Done()
			tickets <- struct{}{}
			defer func() { <-tickets }()
			latest, err := w.Client.Latest(ctx, d.Ecosystem, d.Name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, fmt.Sprintf("%s %s: %v", d.Ecosystem, d.Name, err))
			case latest != "" && Newer(d.Current, latest):
				out = append(out, store.DepReport{
					Ecosystem: d.Ecosystem, Name: d.Name, Current: d.Current, Latest: latest})
			}
		}(d)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Every lookup failing means the registries are unreachable, which is
	// worth recording; a few failing is ordinary.
	if len(errs) == len(found) && len(errs) > 0 {
		return nil, errors.New(errs[0])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// reconcile brings the updates issue in line with what is behind.
func (w *Worker) reconcile(repo store.Repo, behind []store.DepReport) error {
	check, err := w.St.DepCheckFor(repo.ID)
	if err != nil {
		return err
	}
	previous, err := w.St.ReportedDeps(repo.ID)
	if err != nil {
		return err
	}
	return w.maintain(repo, tracked{
		title:   IssueTitle,
		number:  check.IssueNumber,
		record:  func(n int64) error { return w.St.SetDepIssue(repo.ID, n) },
		empty:   len(behind) == 0,
		changed: !same(previous, behind),
		save:    func() error { return w.St.ReplaceDepReports(repo.ID, behind) },
		body:    func() string { return Body(repo.DefaultBranch, behind) },
	})
}

// reconcileAdvisories brings the advisories issue in line with what
// matches.
func (w *Worker) reconcileAdvisories(repo store.Repo, matched []store.DepAdvisory) error {
	check, err := w.St.DepCheckFor(repo.ID)
	if err != nil {
		return err
	}
	previous, err := w.St.ReportedAdvisories(repo.ID)
	if err != nil {
		return err
	}
	return w.maintain(repo, tracked{
		title:   AdvisoryTitle,
		number:  check.AdvisoryIssueNumber,
		record:  func(n int64) error { return w.St.SetDepAdvisoryIssue(repo.ID, n) },
		empty:   len(matched) == 0,
		changed: !sameAdvisories(previous, matched),
		save:    func() error { return w.St.ReplaceDepAdvisories(repo.ID, matched) },
		body:    func() string { return AdvisoryBody(repo.DefaultBranch, matched) },
	})
}

// tracked is one issue the worker maintains for a repository and the set
// it reports.
type tracked struct {
	title   string
	number  int64 // the issue last opened, 0 for none
	record  func(number int64) error
	empty   bool // nothing to report
	changed bool // the set differs from the last report
	save    func() error
	body    func() string
}

// maintain opens the issue when something is first reported, rewrites it
// when the set changes, and closes it when nothing is left.
func (w *Worker) maintain(repo store.Repo, t tracked) error {
	issue, hasIssue := w.openIssue(repo, t.number)
	if t.empty {
		if hasIssue {
			w.St.SetIssueState(issue.ID, "closed")
			t.record(0)
		}
		if t.changed {
			return t.save()
		}
		return nil
	}
	// Nothing changed since the last report: leave it be, whether the issue
	// is still open or the maintainer has closed it. Reopening on an
	// unchanged set would make closing the issue pointless.
	if !t.changed {
		return nil
	}
	if err := t.save(); err != nil {
		return err
	}
	body := t.body()
	if hasIssue {
		if err := w.St.UpdateIssueText(issue.ID, nil, &body, nil); err != nil {
			return err
		}
		w.notify(repo, t.title, issue.Number, fmt.Sprintf("updated issue #%d", issue.Number), body)
		return nil
	}
	author, err := w.St.UserByUsername(store.BotUsername)
	if err != nil {
		// Migration 0028 leaves the account uncreated when the name was
		// already taken, which is the one case worth spelling out: the
		// feature is stuck until an operator frees the name.
		return fmt.Errorf("no %s account to author the issue (the name was taken when this instance upgraded): %w",
			store.BotUsername, err)
	}
	number, err := w.St.CreateIssue(repo.ID, author.ID, t.title, body, "md")
	if err != nil {
		return err
	}
	if err := t.record(number); err != nil {
		return err
	}
	w.St.RecordEvent(repo.ID, author.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, number))
	w.notify(repo, t.title, number, fmt.Sprintf("opened issue #%d", number), body)
	return nil
}

// openIssue loads the issue this worker maintains, if there still is one.
// A closed issue counts as gone: reopening one the maintainer closed would
// be arguing with them, so the next change opens a fresh issue.
func (w *Worker) openIssue(repo store.Repo, number int64) (store.Issue, bool) {
	if number == 0 {
		return store.Issue{}, false
	}
	issue, err := w.St.IssueByNumber(repo.ID, number)
	if err != nil || issue.State != "open" {
		return store.Issue{}, false
	}
	return issue, true
}

// notify files an inbox row for the repo's owners and watchers and mails
// them, the same targets and shape as an issue opened over SSH.
// Best-effort, like every other notification.
func (w *Worker) notify(repo store.Repo, title string, number int64, action, body string) {
	targets, err := w.St.RepoNotifyTargets(repo)
	if err != nil {
		return
	}
	author, err := w.St.UserByUsername(store.BotUsername)
	if err != nil {
		return
	}
	recipients, err := w.St.NotifyRecipients(repo.ID, author.ID, targets, true)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("[%s] #%d: %s", repo.Path(), number, title)
	text := fmt.Sprintf("%s %s\n\n%s\n%s/%s/issues/%d\n", store.BotUsername, action, body,
		strings.TrimSuffix(w.Cfg.Server.SiteURL, "/"), repo.Path(), number)
	path := fmt.Sprintf("%s/issues/%d", repo.Path(), number)
	for _, id := range recipients {
		w.St.AddNotice(id, repo.ID, "issue", store.BotUsername, action, path)
		if w.Cfg.Mail.SMTPHost == "" {
			continue
		}
		email, err := w.St.ActivityMailAddress(id)
		if err != nil || email == "" {
			continue
		}
		w.St.EnqueueMail(email, subject, text)
	}
}
