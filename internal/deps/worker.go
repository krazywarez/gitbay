package deps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/botissue"
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
		w.checkAdvisories(ctx, repo, dir, sha, Resolved(read, root)),
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

// checkAdvisories matches the resolved set against OSV. A Go advisory that
// names packages counts only if a Go file at sha imports one of them; the
// files are read only when such an advisory matched.
func (w *Worker) checkAdvisories(ctx context.Context, repo store.Repo, dir, sha string, resolved []Dep) error {
	if len(resolved) == 0 {
		return w.reconcileAdvisories(repo, nil)
	}
	matched, err := w.Client.Advisories(ctx, resolved)
	if err != nil {
		return fmt.Errorf("advisories: %w", err)
	}
	for _, a := range matched {
		if a.Ecosystem == EcoGo && len(a.Imports) > 0 {
			if imported, ok := GoImports(ctx, dir, sha); ok {
				MarkNotImported(matched, imported)
			}
			break
		}
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
	return botissue.Maintain(w.St, w.Cfg, repo, botissue.Tracked{
		Title:   IssueTitle,
		Number:  check.IssueNumber,
		Record:  func(n int64) error { return w.St.SetDepIssue(repo.ID, n) },
		Empty:   len(behind) == 0,
		Changed: !same(previous, behind),
		Save:    func() error { return w.St.ReplaceDepReports(repo.ID, behind) },
		Body:    func() string { return Body(repo.DefaultBranch, behind) },
	})
}

// reconcileAdvisories brings the advisories issue in line with what
// matches.
func (w *Worker) reconcileAdvisories(repo store.Repo, matched []store.DepAdvisory) error {
	previous, err := w.St.ReportedAdvisories(repo.ID)
	if err != nil {
		return err
	}
	return MaintainAdvisories(w.St, w.Cfg, repo, matched, !sameAdvisories(previous, matched),
		func() error { return w.St.ReplaceDepAdvisories(repo.ID, matched) })
}
