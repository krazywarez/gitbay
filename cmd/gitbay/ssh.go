package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/cliconfig"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/toolpath"
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
	out, err := exec.Command(toolpath.Look("git"), "remote", "get-url", "origin").Output()
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

// sshArgs is every argument before the destination: the port, connection
// multiplexing, and the profile's own options last so they win.
//
// Multiplexing is what makes a CLI over SSH usable: without it every
// command pays a full handshake, seconds on a distant instance, and with
// it the second command in five minutes rides the first's connection
// (#94). The control socket lives under ~/.ssh, which ssh requires to be
// private; a profile can set no_multiplex = true to opt out.
func sshArgs(inst cliconfig.Instance) []string {
	args := []string{}
	if inst.Port != 0 && inst.Port != 22 {
		args = append(args, "-p", strconv.Itoa(inst.Port))
	}
	if !inst.NoMultiplex {
		if home, err := os.UserHomeDir(); err == nil {
			if st, err := os.Stat(filepath.Join(home, ".ssh")); err == nil && st.IsDir() {
				args = append(args,
					"-o", "ControlMaster=auto",
					"-o", "ControlPath="+filepath.Join(home, ".ssh", "gitbay-%C"),
					"-o", "ControlPersist=300")
			}
		}
	}
	return append(args, inst.SSHOptions...)
}

// runSSH executes the server command over the system ssh binary, wiring
// stdio through. It returns the remote exit code.
func runSSH(t target, serverArgv []string, stdin io.Reader) int {
	args := sshArgs(t.inst)
	quoted := make([]string, len(serverArgv))
	for i, a := range serverArgv {
		quoted[i] = shellQuote(a)
	}
	args = append(args, t.inst.SSHUser()+"@"+t.inst.Host, "--", strings.Join(quoted, " "))

	cmd := exec.Command(toolpath.Look("ssh"), args...)
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
			fmt.Fprintln(os.Stderr, "gitbay: ssh could not connect or authenticate; if this worked a moment ago,"+
				" the instance may be rate-limiting authentication after a burst of connections: wait a minute and retry")
			return protocol.ExitProtocol
		}
		return code
	}
	fmt.Fprintln(os.Stderr, "gitbay: running ssh:", err)
	return protocol.ExitProtocol
}

// sshCapture runs a server command and returns its stdout, discarding
// stderr. Used for quiet metadata fetches like issue templates.
func sshCapture(t target, serverArgv []string) (string, int) {
	args := sshArgs(t.inst)
	quoted := make([]string, len(serverArgv))
	for i, a := range serverArgv {
		quoted[i] = shellQuote(a)
	}
	args = append(args, t.inst.SSHUser()+"@"+t.inst.Host, "--", strings.Join(quoted, " "))
	out, err := exec.Command(toolpath.Look("ssh"), args...).Output()
	if err != nil {
		code := protocol.ExitProtocol
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 255 {
			code = ee.ExitCode()
		}
		return "", code
	}
	return string(out), 0
}

// fetchIssueTemplate returns the repo's default issue template body, or ""
// when there is none (or anything fails — prefill is best-effort).
func fetchIssueTemplate(t target, repoPath string) string {
	out, code := sshCapture(t, []string{"issue", "templates", repoPath, "--json"})
	if code != 0 {
		return ""
	}
	var env struct {
		Data []struct {
			Name string `json:"name"`
			Body string `json:"body"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || len(env.Data) == 0 {
		return ""
	}
	for _, tpl := range env.Data {
		if tpl.Name == "issue-template.md" {
			return tpl.Body
		}
	}
	return env.Data[0].Body
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
