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
// against the registries and maintaining one issue per repository.
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

// check compares one repository against the registries and reconciles its
// issue with the result.
func (w *Worker) check(ctx context.Context, repo store.Repo) error {
	dir := w.RepoDir(repo.OwnerName, repo.Name)
	sha, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return nil // empty repo, or no default branch yet
	}
	found := Scan(func(path string) ([]byte, error) {
		return gitutil.ReadBlob(dir, sha, path, manifestLimit)
	})
	if len(found) == 0 {
		return w.reconcile(repo, nil)
	}
	behind, err := w.behind(ctx, found)
	if err != nil {
		return err
	}
	return w.reconcile(repo, behind)
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

// reconcile brings the repository's issue in line with what is behind:
// opened when something first falls behind, rewritten when the set changes,
// closed when nothing is behind any more.
func (w *Worker) reconcile(repo store.Repo, behind []store.DepReport) error {
	check, err := w.St.DepCheckFor(repo.ID)
	if err != nil {
		return err
	}
	previous, err := w.St.ReportedDeps(repo.ID)
	if err != nil {
		return err
	}
	issue, hasIssue := w.openIssue(repo, check.IssueNumber)
	if len(behind) == 0 {
		if hasIssue {
			w.St.SetIssueState(issue.ID, "closed")
			w.St.SetDepIssue(repo.ID, 0)
		}
		if len(previous) > 0 {
			return w.St.ReplaceDepReports(repo.ID, nil)
		}
		return nil
	}
	// Nothing changed since the last report: leave it be, whether the issue
	// is still open or the maintainer has closed it. Reopening on an
	// unchanged set would make closing the issue pointless.
	if same(previous, behind) {
		return nil
	}
	if err := w.St.ReplaceDepReports(repo.ID, behind); err != nil {
		return err
	}
	body := Body(repo.DefaultBranch, behind)
	if hasIssue {
		if err := w.St.UpdateIssueText(issue.ID, nil, &body, nil); err != nil {
			return err
		}
		w.notify(repo, issue.Number, fmt.Sprintf("updated issue #%d", issue.Number), body)
		return nil
	}
	author, err := w.St.UserByUsername(store.BotUsername)
	if err != nil {
		return fmt.Errorf("loading %s: %w", store.BotUsername, err)
	}
	number, err := w.St.CreateIssue(repo.ID, author.ID, IssueTitle, body, "md")
	if err != nil {
		return err
	}
	if err := w.St.SetDepIssue(repo.ID, number); err != nil {
		return err
	}
	w.St.RecordEvent(repo.ID, author.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, number))
	w.notify(repo, number, fmt.Sprintf("opened issue #%d", number), body)
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

// notify mails the repo's owners, the same targets and shape as an issue
// filed over SSH. Best-effort, like every other notification.
func (w *Worker) notify(repo store.Repo, number int64, action, body string) {
	if w.Cfg.Mail.SMTPHost == "" {
		return
	}
	targets, err := w.St.RepoNotifyTargets(repo)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("[%s] #%d: %s", repo.Path(), number, IssueTitle)
	text := fmt.Sprintf("%s %s\n\n%s\n%s/%s/issues/%d\n", store.BotUsername, action, body,
		strings.TrimSuffix(w.Cfg.Server.SiteURL, "/"), repo.Path(), number)
	for _, id := range targets {
		email, err := w.St.PrimaryVerifiedEmail(id)
		if err != nil || email == "" {
			continue
		}
		w.St.EnqueueMail(email, subject, text)
	}
}
