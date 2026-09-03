package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// isoNow is the timestamp format schedules are compared in.
func isoNow(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// NextRun returns the next firing time for a cron expression as a stored
// timestamp. The caller has already validated the expression.
func NextRun(expr string, after time.Time) string {
	c, err := ParseCron(expr)
	if err != nil {
		return isoNow(after.Add(24 * time.Hour)) // unreachable after Parse validation
	}
	n := c.Next(after)
	if n.IsZero() {
		return isoNow(after.AddDate(1, 0, 0))
	}
	return isoNow(n)
}

// Scheduler fires scheduled builds. repoDir maps a repo to its bare path.
type Scheduler struct {
	St      *store.Store
	RepoDir func(owner, name string) string
	SiteURL string
}

// Run ticks until the context ends. GITBAY_SCHED_TICK overrides the
// interval for tests.
func (s *Scheduler) Run(ctx context.Context) {
	tick := time.Minute
	if v := os.Getenv("GITBAY_SCHED_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunDue(time.Now())
		}
	}
}

// RunDue queues every due scheduled build and advances its next_run, and
// fails any build a runner claimed and never reported. Split from the
// ticker for tests.
func (s *Scheduler) RunDue(now time.Time) {
	s.reapStale()
	due, err := s.St.DueSchedules(isoNow(now))
	if err != nil {
		slog.Error("scheduler: listing due builds", "err", err)
		return
	}
	for _, e := range due {
		repo, err := s.St.RepoByID(e.RepoID)
		if err != nil {
			s.St.RemoveSchedule(e.RepoID, e.Job) // repo gone
			continue
		}
		// Always advance first so a broken repo cannot wedge the loop.
		s.St.SetScheduleNext(e.RepoID, e.Job, NextRun(e.Cron, now))
		dir := s.RepoDir(repo.OwnerName, repo.Name)
		sha, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
		if err != nil {
			continue // empty repo
		}
		raw, err := gitutil.ReadBlob(dir, sha, ConfigPath, 1<<16)
		if err != nil {
			s.St.RemoveSchedule(e.RepoID, e.Job) // config removed
			continue
		}
		jobs, err := Parse(raw)
		if err != nil {
			continue
		}
		var job *Job
		for i := range jobs {
			if jobs[i].Name == e.Job && jobs[i].Schedule != "" {
				job = &jobs[i]
				break
			}
		}
		if job == nil {
			s.St.RemoveSchedule(e.RepoID, e.Job)
			continue
		}
		steps, _ := json.Marshal(job.Steps)
		n, err := s.St.CreateBuild(repo.ID, job.Name, sha, repo.DefaultBranch, string(steps), true)
		if err != nil {
			slog.Error("scheduler: queueing build", "repo", repo.Path(), "job", job.Name, "err", err)
			continue
		}
		url := fmt.Sprintf("%s/%s/builds/%d", s.SiteURL, repo.Path(), n)
		s.St.SetCommitStatus(repo.ID, sha, "ci/"+job.Name, "pending", "scheduled", url, 0)
	}
}

// reapStale resolves builds running past the deadline, so a runner that
// died mid-build leaves neither a build running nor a commit pending
// forever. It runs on the tick rather than on the next runner claim, so it
// does not need a runner to be alive.
func (s *Scheduler) reapStale() {
	stale, err := s.St.ReapStaleBuilds()
	if err != nil {
		slog.Error("scheduler: reaping stale builds", "err", err)
		return
	}
	for _, b := range stale {
		repo, err := s.St.RepoByID(b.RepoID)
		if err != nil {
			continue
		}
		url := fmt.Sprintf("%s/%s/builds/%d", s.SiteURL, repo.Path(), b.Number)
		s.St.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, "failure", "build abandoned", url, 0)
		slog.Warn("build abandoned", "repo", repo.Path(), "build", b.Number, "job", b.Job)
	}
}
