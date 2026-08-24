// Package mirror synchronizes repositories with foreign remotes: push
// mirrors propagate local refs outward after each receive, pull mirrors
// keep a local copy fresh from an upstream. Sync runs in a background
// worker, never in the push path; outcomes are recorded per mirror so
// `repo mirror list` shows failure states like webhook deliveries do.
package mirror

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

const askpassScript = `#!/bin/sh
case "$1" in
  Username*) echo "${GITBAY_MIRROR_USER}" ;;
  *)         echo "${GITBAY_MIRROR_TOKEN}" ;;
esac
`

type Worker struct {
	St   *store.Store
	Cfg  config.Config
	Tick time.Duration
}

func New(st *store.Store, cfg config.Config) *Worker {
	tick := 10 * time.Second
	if v := os.Getenv("GITBAY_MIRROR_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Worker{St: st, Cfg: cfg, Tick: tick}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.sweep()
		}
	}
}

func (w *Worker) sweep() {
	interval := w.Cfg.Mirrors.PullIntervalMinutes * 60
	due, err := w.St.DueMirrors(interval)
	if err != nil {
		slog.Error("mirror: listing due", "err", err)
		return
	}
	for _, m := range due {
		if err := w.sync(m); err != nil {
			slog.Warn("mirror sync failed", "mirror", m.ID, "url", m.URL, "err", err)
			w.St.SetMirrorResult(m.ID, err.Error())
		} else {
			w.St.SetMirrorResult(m.ID, "")
		}
	}
}

func (w *Worker) sync(m store.Mirror) error {
	repo, err := w.St.RepoByID(m.RepoID)
	if err != nil {
		return err
	}
	dir := control.RepoDir(w.Cfg.Server.Root, repo.OwnerName, repo.Name)

	env := []string{"GIT_TERMINAL_PROMPT=0", "HOME=" + w.Cfg.Server.Root}
	if m.Token != "" {
		askpass := filepath.Join(w.Cfg.Server.Root, "mirror-askpass.sh")
		if err := os.WriteFile(askpass, []byte(askpassScript), 0o700); err != nil {
			return err
		}
		user := m.Username
		if user == "" {
			user = "x-access-token"
		}
		env = append(env,
			"GIT_ASKPASS="+askpass,
			"GITBAY_MIRROR_USER="+user,
			"GITBAY_MIRROR_TOKEN="+m.Token)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var args []string
	if m.Direction == "push" {
		// Branches and tags only: internal refs (merge-requests) stay home.
		args = []string{"-C", dir, "push", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}
	} else {
		args = []string{"-C", dir, "fetch", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %.300s", m.Direction, err, out)
	}
	return nil
}
