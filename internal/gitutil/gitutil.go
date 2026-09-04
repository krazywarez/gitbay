// Package gitutil wraps the system git binary. All repository access goes
// through git subprocesses; there is no in-process git implementation.
package gitutil

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// InitBare creates a bare repository with the shared hooks directory wired
// via core.hooksPath.
func InitBare(path, defaultBranch, hooksPath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	cmd := exec.Command(toolpath.Look("git"), "init", "--bare", "--initial-branch="+defaultBranch, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %v\n%s", err, out)
	}
	// An empty hooksPath leaves the bare repo with no hooks — used for
	// companion repos (wikis) that carry no ref policy.
	if hooksPath != "" {
		cmd = exec.Command(toolpath.Look("git"), "-C", path, "config", "core.hooksPath", hooksPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config core.hooksPath: %v\n%s", err, out)
		}
	}
	return nil
}

// Transport streams one git transport service (upload-pack, receive-pack,
// upload-archive). extraEnv entries are appended to the process environment;
// hooks read the GITBAY_* variables from it. maxPack caps incoming pack
// bytes on receive-pack (0 = unlimited).
func Transport(service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string, maxPack int64) error {
	var args []string
	switch service {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
		if service == "git-receive-pack" && maxPack > 0 {
			args = []string{"-c", fmt.Sprintf("receive.maxInputSize=%d", maxPack)}
		}
		args = append(args, strings.TrimPrefix(service, "git-"), repoPath)
	default:
		return fmt.Errorf("unknown service %q", service)
	}
	cmd := exec.Command(toolpath.Look("git"), args...)
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
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "merge-base", "--is-ancestor", old, new)
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
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "rev-list", fmt.Sprintf("--max-count=%d", limit), "--end-of-options", ref)
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

// RevListPath returns up to limit commit SHAs reachable from ref that
// touch filePath, newest first. The "--" keeps the path from ever being
// read as an option or ref.
func RevListPath(dir, ref, filePath string, limit int) ([]string, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "rev-list",
		fmt.Sprintf("--max-count=%d", limit), "--end-of-options", ref, "--", filePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rev-list %s -- %s: %w", ref, filePath, err)
	}
	var shas []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			shas = append(shas, l)
		}
	}
	return shas, nil
}

// PeelToCommit resolves a ref or object to its commit — annotated tags
// peel to the commit they point at.
func PeelToCommit(dir, ref string) (string, error) {
	out, err := exec.Command(toolpath.Look("git"), "-C", dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s^{commit}: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ReadCommit returns the raw commit object bytes.
func ReadCommit(dir, sha string) ([]byte, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "cat-file", "commit", "--end-of-options", sha)
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
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "fetch", "--progress", "--no-write-fetch-head", url,
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

// FetchPullHeads pulls a GitHub repository's pull-request heads into
// refs/gh-pull/*, so an imported pull request has something to diff.
// GitHub publishes every PR head at refs/pull/<n>/head on the git remote,
// but a mirror made with the default refspecs does not carry them, which
// is why an import used to produce merge requests with no head at all.
//
// One fetch for every pull request rather than one each: the ref count is
// the repository's history, and asking a hundred times is a hundred
// handshakes. extraEnv carries credentials via GIT_ASKPASS; the URL must
// never contain them.
func FetchPullHeads(ctx context.Context, dir, url string, errW io.Writer, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "fetch", "--no-write-fetch-head", "--no-tags",
		url, "+refs/pull/*/head:refs/gh-pull/*")
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stderr = errW
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetch pull heads from %s: %w", url, err)
	}
	return nil
}

// RemoteDefaultBranch asks the remote which branch HEAD points at.
func RemoteDefaultBranch(ctx context.Context, url string, extraEnv []string) (string, error) {
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "ls-remote", "--symref", url, "HEAD")
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
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("symbolic-ref: %v\n%s", err, out)
	}
	return nil
}

// gitDefaultDescription is the placeholder git init writes; treated as no
// description at all.
const gitDefaultDescription = "Unnamed repository; edit this file 'description' to name the repository."

// ReadDescription returns the repo's description from the classic
// <repo>.git/description file, empty for the git-init placeholder.
func ReadDescription(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "description"))
	if err != nil {
		return ""
	}
	desc := strings.TrimSpace(string(raw))
	if desc == gitDefaultDescription {
		return ""
	}
	return desc
}

// WriteDescription sets the description file: first line only, capped.
func WriteDescription(dir, desc string) error {
	desc, _, _ = strings.Cut(strings.TrimSpace(desc), "\n")
	if len(desc) > 256 {
		desc = desc[:256]
	}
	return os.WriteFile(filepath.Join(dir, "description"), []byte(desc+"\n"), 0o644)
}

// DirSize sums file sizes under dir; unreadable entries count as zero.
func DirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// Every git this package runs prints paths as they are, not quoted with
// octal escapes the way core.quotepath does by default, so a file called
// übersicht.txt lists, greps, blames and diffs under its own name.
// GIT_CONFIG_PARAMETERS reaches every subprocess, hooks included,
// without touching each call site (#129).
func init() {
	const q = "'core.quotepath=off'"
	if cur := os.Getenv("GIT_CONFIG_PARAMETERS"); cur != "" {
		os.Setenv("GIT_CONFIG_PARAMETERS", cur+" "+q)
	} else {
		os.Setenv("GIT_CONFIG_PARAMETERS", q)
	}
}
