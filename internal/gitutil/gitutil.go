// Package gitutil wraps the system git binary. All repository access goes
// through git subprocesses; there is no in-process git implementation.
package gitutil

import (
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
// upload-archive) over rw. extraEnv entries are appended to the daemon's
// environment; hooks read the FORGE_* variables from it.
func Transport(service, repoPath string, rw io.ReadWriter, errW io.Writer, extraEnv []string) error {
	var args []string
	switch service {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
		args = []string{strings.TrimPrefix(service, "git-"), repoPath}
	default:
		return fmt.Errorf("unknown service %q", service)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = rw
	cmd.Stdout = rw
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
