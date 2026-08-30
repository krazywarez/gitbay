package main

import (
	"bytes"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/buildinfo"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
)

func capture(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestLogBuildWarnsOnAnUncommittedBuild(t *testing.T) {
	prev := buildinfo.Commit
	defer func() { buildinfo.Commit = prev }()

	buildinfo.Commit = "abc123abc123"
	if out := capture(t, logBuild); !strings.Contains(out, "level=INFO") {
		t.Errorf("a committed build should log INFO:\n%s", out)
	}

	buildinfo.Commit = "abc123abc123-dirty"
	out := capture(t, logBuild)
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("a dirty build should log WARN:\n%s", out)
	}
	if !strings.Contains(out, "abc123abc123-dirty") {
		t.Errorf("the warning should name the build:\n%s", out)
	}
}

// sourceRepo builds a bare repository holding one commit on the default
// branch, plus one commit off it, and returns the config pointing at it.
func sourceRepo(t *testing.T) (cfg config.Config, onBranch, offBranch string) {
	t.Helper()
	root := t.TempDir()
	dir := control.RepoDir(root, "krz", "gitbay")

	work := filepath.Join(t.TempDir(), "work")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-q", "-b", "main", work)
	run("-C", work, "commit", "-q", "--allow-empty", "-m", "on the branch")
	onBranch = run("-C", work, "rev-parse", "HEAD")
	run("-C", work, "checkout", "-q", "-b", "side")
	run("-C", work, "commit", "-q", "--allow-empty", "-m", "off the branch")
	offBranch = run("-C", work, "rev-parse", "HEAD")

	run("clone", "-q", "--bare", work, dir)
	run("-C", dir, "symbolic-ref", "HEAD", "refs/heads/main")

	cfg = config.Config{Server: config.Server{Root: root, SourceRepo: "krz/gitbay"}}
	return cfg, onBranch, offBranch
}

func TestWarnIfUnmerged(t *testing.T) {
	prev := buildinfo.Commit
	defer func() { buildinfo.Commit = prev }()

	cfg, onBranch, offBranch := sourceRepo(t)

	buildinfo.Commit = onBranch
	if out := capture(t, func() { warnIfUnmerged(cfg) }); out != "" {
		t.Errorf("a build on the default branch should be silent:\n%s", out)
	}

	buildinfo.Commit = offBranch
	out := capture(t, func() { warnIfUnmerged(cfg) })
	if !strings.Contains(out, "not on the source repository") {
		t.Errorf("a build off the default branch should warn:\n%s", out)
	}

	// A commit the repository has never seen cannot be vouched for.
	buildinfo.Commit = "0123456789abcdef0123456789abcdef01234567"
	if out := capture(t, func() { warnIfUnmerged(cfg) }); !strings.Contains(out, "cannot check") {
		t.Errorf("an unknown commit should warn:\n%s", out)
	}
}

func TestWarnIfUnmergedIsSilentWithoutASourceRepo(t *testing.T) {
	prev := buildinfo.Commit
	defer func() { buildinfo.Commit = prev }()
	buildinfo.Commit = "abc123abc123"

	// The default for every instance that does not host its own source.
	cfg := config.Config{Server: config.Server{Root: t.TempDir()}}
	if out := capture(t, func() { warnIfUnmerged(cfg) }); out != "" {
		t.Errorf("no source_repo should mean no check:\n%s", out)
	}

	// A dirty build has already been warned about by logBuild; do not warn twice.
	cfg.Server.SourceRepo = "krz/gitbay"
	buildinfo.Commit = "abc123abc123-dirty"
	if out := capture(t, func() { warnIfUnmerged(cfg) }); out != "" {
		t.Errorf("a dirty build is logBuild's to report, not this one's:\n%s", out)
	}
}
