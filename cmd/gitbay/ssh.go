package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/cliconfig"
	"gitbay.org/gitbay/internal/protocol"
)

// context is the resolved target for a command: which instance to talk to
// and, when run inside a clone of a forge repo, which repository.
type target struct {
	inst cliconfig.Instance
	repo string // owner/name, "" when not inferable
}

// resolveTarget picks the instance and repo. An origin remote matching a
// CONFIGURED instance wins and carries repo inference; otherwise the
// default instance is used with no inference — a clone from some other
// host (github, a different forge) must never hijack the command. The
// raw origin serves as an ad-hoc instance only when nothing is configured
// at all.
func resolveTarget() (target, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return target{}, err
	}

	parsed, repo, originOK := cliconfig.ParseRemoteURL(originURL())
	if originOK {
		norm := func(p int) int {
			if p == 0 {
				return 22
			}
			return p
		}
		for _, inst := range cfg.Instances {
			if inst.Host == parsed.Host && norm(inst.Port) == norm(parsed.Port) {
				return target{inst: inst, repo: repo}, nil
			}
		}
	}

	if inst, _, err := cfg.DefaultInstance(); err == nil {
		return target{inst: inst}, nil
	}
	if originOK {
		return target{inst: parsed, repo: repo}, nil
	}
	return target{}, fmt.Errorf("no gitbay instance configured; run: gitbay remote add <name> <host>")
}

func originURL() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// bareWord matches arguments that need no quoting for the server-side
// POSIX tokenizer.
var bareWord = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_!-]+$`)

// shellQuote quotes one argument for the SSH command string; the server
// tokenizes with POSIX rules and no expansion.
func shellQuote(arg string) string {
	if arg != "" && bareWord.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// runSSH executes the server command over the system ssh binary, wiring
// stdio through. It returns the remote exit code.
func runSSH(t target, serverArgv []string, stdin io.Reader) int {
	args := []string{}
	if t.inst.Port != 0 && t.inst.Port != 22 {
		args = append(args, "-p", strconv.Itoa(t.inst.Port))
	}
	args = append(args, t.inst.SSHOptions...)
	quoted := make([]string, len(serverArgv))
	for i, a := range serverArgv {
		quoted[i] = shellQuote(a)
	}
	args = append(args, t.inst.SSHUser()+"@"+t.inst.Host, "--", strings.Join(quoted, " "))

	cmd := exec.Command("ssh", args...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code == 255 { // ssh-level failure (connection, auth, host key)
			return protocol.ExitProtocol
		}
		return code
	}
	fmt.Fprintln(os.Stderr, "gitbay: running ssh:", err)
	return protocol.ExitProtocol
}

// withRepo prepends the repo path to args unless the user already gave one
// explicitly (a first argument containing '/'). Commands' server parsers
// accept the path at any position, so the front is always safe.
func withRepo(t target, args []string) ([]string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && strings.Contains(args[0], "/") {
		return args, nil // explicit owner/name
	}
	if t.repo == "" {
		return nil, fmt.Errorf("no repository given and none inferable: pass <owner/name> or run inside a clone of a gitbay repository")
	}
	return append([]string{t.repo}, args...), nil
}
