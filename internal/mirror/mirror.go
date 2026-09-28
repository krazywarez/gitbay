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
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/toolpath"
	"gitbay.org/gitbay/internal/webhook"
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
	// Lookup resolves a mirror's host immediately before each sync.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

func New(st *store.Store, cfg config.Config) *Worker {
	tick := 10 * time.Second
	if v := os.Getenv("GITBAY_MIRROR_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Worker{St: st, Cfg: cfg, Tick: tick,
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}}
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
	u, err := url.Parse(m.URL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("mirror URL scheme %q is not http or https", u.Scheme)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// The URL was checked when saved, but DNS can answer differently
	// now. Check what it resolves to at sync time, then let git connect
	// to exactly those addresses.
	ips, err := w.Lookup(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("resolving %s: %w", u.Hostname(), err)
	}
	if len(ips) == 0 {
		// An empty resolve list would leave curl to resolve the host itself.
		return fmt.Errorf("%s resolves to no address", u.Hostname())
	}
	if err := webhook.CheckAddrs(u.Hostname(), ips, w.Cfg.Webhooks.AllowLocal); err != nil {
		return err
	}

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

	args := append(pinArgs(u, ips), "-C", dir)
	if m.Direction == "push" {
		// Branches and tags only: internal refs (merge-requests) stay home.
		args = append(args, "push", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	} else {
		args = append(args, "fetch", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	}
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %.300s", m.Direction, err, out)
	}
	return nil
}

// pinArgs keeps git on the addresses just checked: curl's resolve list
// pins the host, and with redirects off a server cannot send git on to
// a host nobody checked. An address literal needs no pin.
func pinArgs(u *url.URL, ips []net.IP) []string {
	args := []string{"-c", "http.followRedirects=false"}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return args
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		if ip.To4() == nil {
			addrs[i] = "[" + ip.String() + "]"
		} else {
			addrs[i] = ip.String()
		}
	}
	return append(args, "-c", "http.curloptResolve="+host+":"+port+":"+strings.Join(addrs, ","))
}
