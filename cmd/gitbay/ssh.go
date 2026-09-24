package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

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

// noColor is --no-color, stripped from argv in main.
var noColor bool

// termValue is GITBAY_TERM for this invocation: the terminal's width,
// and whether colour is wanted. Empty when stdout is not a terminal,
// so piped output stays the rows stock ssh prints.
func termValue(isTerminal bool, cols int, env func(string) string) string {
	if !isTerminal || cols < 40 {
		return ""
	}
	v := strconv.Itoa(cols)
	if !noColor && env("NO_COLOR") == "" && env("TERM") != "dumb" {
		v += ",color"
	}
	return v
}

// stripNoColor removes --no-color wherever it appears.
func stripNoColor(args []string) ([]string, bool) {
	out := args[:0:0]
	found := false
	for _, a := range args {
		if a == "--no-color" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// pagerArgv is the pager to run long output through: GITBAY_PAGER, then
// PAGER, then less. An empty GITBAY_PAGER turns paging off.
func pagerArgv(env func(string) (string, bool)) []string {
	if v, ok := env("GITBAY_PAGER"); ok {
		return strings.Fields(v)
	}
	if v, ok := env("PAGER"); ok && v != "" {
		return strings.Fields(v)
	}
	return []string{"less"}
}

// pages reports whether a command's output goes through the pager at a
// terminal: views, diffs and logs, never a follow or JSON.
func pages(server, args []string) bool {
	if len(server) == 0 || slices.Contains(args, "--json") || slices.Contains(args, "--follow") {
		return false
	}
	switch server[len(server)-1] {
	case "show", "diff", "log":
		return true
	}
	return false
}

// runSSH executes the server command over the system ssh binary, wiring
// stdio through, with no pager. It returns the remote exit code.
func runSSH(t target, serverArgv []string, stdin io.Reader) int {
	return runSSHPaged(t, serverArgv, stdin, false)
}

// runSSHPaged is runSSH with output optionally run through the pager when
// stdout is a terminal and page is true. It returns the remote exit code.
func runSSHPaged(t target, serverArgv []string, stdin io.Reader, page bool) int {
	args := sshArgs(t.inst)

	fd := int(os.Stdout.Fd())
	cols := 0
	isTTY := term.IsTerminal(fd)
	if isTTY {
		cols, _, _ = term.GetSize(fd)
	}
	if v := termValue(isTTY, cols, os.Getenv); v != "" && !slices.Contains(serverArgv, "--json") {
		serverArgv = append([]string{"--term=" + v}, serverArgv...)
	}

	quoted := make([]string, len(serverArgv))
	for i, a := range serverArgv {
		quoted[i] = shellQuote(a)
	}
	args = append(args, t.inst.SSHUser()+"@"+t.inst.Host, "--", strings.Join(quoted, " "))

	cmd := exec.Command(toolpath.Look("ssh"), args...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	var pager *exec.Cmd
	var pw io.WriteCloser
	if page && isTTY {
		if argv := pagerArgv(os.LookupEnv); len(argv) > 0 {
			pager = exec.Command(toolpath.Look(argv[0]), argv[1:]...)
			pager.Stdout, pager.Stderr = os.Stdout, os.Stderr
			if _, ok := os.LookupEnv("LESS"); !ok {
				pager.Env = append(os.Environ(), "LESS=FRX")
			}
			if w, err := pager.StdinPipe(); err == nil && pager.Start() == nil {
				pw = w
				cmd.Stdout = pw
			} else {
				pager = nil
			}
		}
	}

	err := cmd.Run()
	if pager != nil {
		pw.Close()
		pager.Wait()
	}
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if pager != nil {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGPIPE {
				// The user quit the pager before ssh finished writing;
				// that is not a failure of the command itself.
				return 0
			}
		}
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
