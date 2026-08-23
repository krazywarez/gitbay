// Package gitutil wraps the system git binary. All repository access goes
// through git subprocesses; there is no in-process git implementation.
package gitutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InitBare creates a bare repository with the shared hooks directory wired
// via core.hooksPath.
func InitBare(path, defaultBranch, hooksPath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	cmd := exec.Command("git", "init", "--bare", "--initial-branch="+defaultBranch, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", path, "config", "core.hooksPath", hooksPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config core.hooksPath: %v\n%s", err, out)
	}
	return nil
}

// Transport streams one git transport service (upload-pack, receive-pack,
// upload-archive). extraEnv entries are appended to the process environment;
// hooks read the FORGE_* variables from it.
func Transport(service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string) error {
	var args []string
	switch service {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
		args = []string{strings.TrimPrefix(service, "git-"), repoPath}
	default:
		return fmt.Errorf("unknown service %q", service)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = errW
	return cmd.Run()
}

// IsAncestor reports whether old is an ancestor of new in the repository at
// dir. It must run with the caller's environment intact so that quarantined
// objects during pre-receive remain visible.
func IsAncestor(dir, old, new string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", old, new)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// ZeroSHA reports whether s is an all-zero object id (SHA-1 or SHA-256).
func ZeroSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// RevList returns up to limit commit SHAs reachable from ref, newest first.
func RevList(dir, ref string, limit int) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-list", fmt.Sprintf("--max-count=%d", limit), ref)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rev-list %s: %w", ref, err)
	}
	var shas []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			shas = append(shas, l)
		}
	}
	return shas, nil
}

// ReadCommit returns the raw commit object bytes.
func ReadCommit(dir, sha string) ([]byte, error) {
	cmd := exec.Command("git", "-C", dir, "cat-file", "commit", sha)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cat-file commit %s: %w", sha, err)
	}
	return out, nil
}

// FetchMirror pulls all branches, tags, and notes from a foreign URL into
// the bare repository at dir, forcing updates. Progress streams to errW so
// an interactive caller can watch. extraEnv carries credentials via
// GIT_ASKPASS; the URL itself must never contain them.
func FetchMirror(ctx context.Context, dir, url string, errW io.Writer, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--progress", "--no-write-fetch-head", url,
		"+refs/heads/*:refs/heads/*",
		"+refs/tags/*:refs/tags/*",
		"+refs/notes/*:refs/notes/*")
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stderr = errW
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetch from %s: %w", url, err)
	}
	return nil
}

// RemoteDefaultBranch asks the remote which branch HEAD points at.
func RemoteDefaultBranch(ctx context.Context, url string, extraEnv []string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", url, "HEAD")
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ls-remote %s: %w", url, err)
	}
	// "ref: refs/heads/<branch>\tHEAD"
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if branch, _, ok := strings.Cut(rest, "\t"); ok {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("remote %s did not advertise a default branch", url)
}

// SetHead points the bare repo's HEAD at a branch.
func SetHead(dir, branch string) error {
	cmd := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("symbolic-ref: %v\n%s", err, out)
	}
	return nil
}
