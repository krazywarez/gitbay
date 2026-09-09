package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// initOut is where init prints; tests capture it.
var initOut io.Writer = os.Stdout

// runInit makes a fresh install ready to attach: a key of its own, a
// config file the service reads, and the one command to run next. It never
// overwrites a key or a config that exists, so running it twice is safe.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(initOut)
	remote := fs.String("remote", "git@gitbay.org", "ssh destination of the gitbay server")
	workdir := fs.String("workdir", defaultWorkdir(), "build workspace root")
	isolation := fs.String("isolation", isolationNone, "how steps run: none, or podman with -image")
	image := fs.String("image", "", "container image for -isolation podman")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *isolation == isolationPodman && *image == "" {
		fmt.Fprintln(initOut, "-isolation podman needs -image <ref>: the runner refuses to start without one, and there is no image to guess")
		return 2
	}
	if *isolation != isolationPodman && *isolation != isolationNone {
		fmt.Fprintf(initOut, "unknown isolation %q\n", *isolation)
		return 2
	}

	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(initOut, err)
		return 1
	}
	os.Chmod(dir, 0o700)
	key := filepath.Join(dir, "id_ed25519")
	if !fileExists(key) {
		cmd := exec.Command(toolpath.Look("ssh-keygen"), "-q", "-t", "ed25519", "-N", "", "-C", "gitbay-runner", "-f", key)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(initOut, "ssh-keygen: %v\n%s", err, out)
			return 1
		}
	}
	os.Chmod(key, 0o600)

	cfgPath := filepath.Join(dir, "config.toml")
	if !fileExists(cfgPath) {
		var b strings.Builder
		fmt.Fprintf(&b, "remote = %q\n", *remote)
		fmt.Fprintf(&b, "workdir = %q\n", *workdir)
		fmt.Fprintf(&b, "isolation = %q\n", *isolation)
		if *image != "" {
			fmt.Fprintf(&b, "image = %q\n", *image)
		}
		fmt.Fprintf(&b, "untrusted = false\n")
		fmt.Fprintf(&b, "identity = %q\n", key)
		if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
			fmt.Fprintln(initOut, err)
			return 1
		}
	}

	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		fmt.Fprintln(initOut, err)
		return 1
	}
	host := *remote
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	fmt.Fprintf(initOut, "config: %s\nkey:    %s\n\n", cfgPath, key)
	if *isolation == isolationNone {
		fmt.Fprintln(initOut, "Steps run on this machine as your user, with no container. Untrusted builds\n(merge requests from forks) are excluded unless the runner is started with\n-untrusted, so that means your own commits.")
	}
	fmt.Fprintf(initOut, "This runner's public key:\n\n  %s\nAttach it to each repository it should build, as a repository admin:\n\n  gitbay repo runner add owner/name < %s.pub\n\nor paste it under Runners at https://%s/owner/name/settings\n\nThen start it:\n\n  brew services start krz/tap/gitbay-runner\n\nor run gitbay-runner with no arguments.\n",
		strings.TrimSpace(string(pub)), key, host)
	return 0
}
