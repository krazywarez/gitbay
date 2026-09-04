package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"gitbay.org/gitbay/internal/cliconfig"
	"gitbay.org/gitbay/internal/protocol"
)

// hasBodyFlag reports whether args already carry body/message input.
func hasBodyFlag(args []string) bool {
	for _, a := range args {
		if a == "--body" || a == "--message" || a == "--file" {
			return true
		}
	}
	return false
}

// maybeEditor opens $EDITOR for long text when the command usually wants a
// body, none was given, and we are on a terminal. The result is passed to
// the server via --file - on stdin. Returns the (possibly extended) args,
// the stdin to use, and ok=false if the user aborted.
func maybeEditor(args []string, kind string, prefill func() string) ([]string, *strings.Reader, bool, error) {
	if hasBodyFlag(args) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return args, nil, true, nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		// No editor configured: proceed with an empty body rather than
		// failing — bodies are optional everywhere.
		return args, nil, true, nil
	}
	f, err := os.CreateTemp("", "gitbay-"+kind+"-*.md")
	if err != nil {
		return nil, nil, false, err
	}
	defer os.Remove(f.Name())
	if prefill != nil {
		if body := prefill(); body != "" {
			fmt.Fprintf(f, "%s\n", strings.TrimRight(body, "\n"))
		}
	}
	fmt.Fprintf(f, "\n# Write the %s body above. Lines starting with '#' are ignored.\n# Save an empty file to skip the body.\n", kind)
	f.Close()

	ed := exec.Command("sh", "-c", editor+" "+shellQuote(f.Name()))
	ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ed.Run(); err != nil {
		return nil, nil, false, fmt.Errorf("editor: %w", err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, nil, false, err
	}
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		body.WriteString(line + "\n")
	}
	text := strings.TrimSpace(body.String())
	if text == "" {
		return args, nil, true, nil
	}
	return append(args, "--file", "-"), strings.NewReader(text + "\n"), true, nil
}

func runGitLocal(args ...string) int {
	cmd := exec.Command("git", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	return 0
}

// cmdRepoClone implements `forge repo clone <owner/name> [dir]`.
func cmdRepoClone(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: gitbay repo clone <owner/name> [dir]")
		return protocol.ExitUsage
	}
	t, err := resolveTarget()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	gitArgs := append([]string{"clone", t.inst.CloneURL(args[0])}, args[1:]...)
	if len(t.inst.SSHOptions) > 0 {
		os.Setenv("GIT_SSH_COMMAND", "ssh "+strings.Join(quoteAll(t.inst.SSHOptions), " "))
	}
	return runGitLocal(gitArgs...)
}

// cmdMRCheckout implements `forge mr checkout <n>`: fetch the MR head from
// origin and check it out as a local branch.
func cmdMRCheckout(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gitbay mr checkout <n>")
		return protocol.ExitUsage
	}
	n := args[0]
	ref := "refs/merge-requests/" + n + "/head"
	if code := runGitLocal("fetch", "origin", ref); code != 0 {
		return code
	}
	return runGitLocal("checkout", "-B", "mr/"+n, "FETCH_HEAD")
}

// cmdInit implements `forge init [name] [--private]`: git init if needed,
// create the repository on the default instance, and point origin at it.
func cmdInit(args []string) int {
	var name string
	private := false
	for _, a := range args {
		switch {
		case a == "--private":
			private = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(os.Stderr, "usage: gitbay init [name] [--private]")
			return protocol.ExitUsage
		default:
			name = a
		}
	}
	if name == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitFailure
		}
		name = filepath.Base(wd)
	}

	cfg, err := cliconfig.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	inst, _, err := cfg.DefaultInstance()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	t := target{inst: inst}

	// The server requires owner = the authenticated user; ask who that is.
	whoami, code := captureSSH(t, []string{"whoami"})
	if code != 0 {
		return code
	}
	username := strings.TrimSpace(whoami)
	repoPath := username + "/" + name

	createArgs := []string{"repo", "create", repoPath}
	if private {
		createArgs = append(createArgs, "--private")
	}
	if code := runSSH(t, createArgs, strings.NewReader("")); code != 0 {
		return code
	}

	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		if code := runGitLocal("init", "-q", "-b", "main"); code != 0 {
			return code
		}
	}
	url := inst.CloneURL(repoPath)
	if code := runGitLocal("remote", "add", "origin", url); code != 0 {
		return code
	}
	fmt.Printf("origin -> %s\npush with: git push -u origin main\n", url)
	return 0
}

// captureSSH runs a server command and returns its stdout.
func captureSSH(t target, serverArgv []string) (string, int) {
	args := sshArgs(t.inst)
	quoted := quoteAll(serverArgv)
	args = append(args, t.inst.SSHUser()+"@"+t.inst.Host, "--", strings.Join(quoted, " "))
	cmd := exec.Command("ssh", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "gitbay: running ssh:", err)
		return "", protocol.ExitProtocol
	}
	return string(out), 0
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return out
}

// cmdRemoteAdd implements `gitbay remote add <name> <host> [flags]`.
func cmdRemoteAdd(args []string) int {
	var name, host, user string
	var port int
	var setDefault bool
	var sshOptions []string
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "--port":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--port requires a value")
				return protocol.ExitUsage
			}
			fmt.Sscanf(args[i+1], "%d", &port)
			i += 2
		case "--user":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--user requires a value")
				return protocol.ExitUsage
			}
			user = args[i+1]
			i += 2
		case "--ssh-option":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--ssh-option requires a value")
				return protocol.ExitUsage
			}
			sshOptions = append(sshOptions, args[i+1])
			i += 2
		case "--default":
			setDefault = true
			i++
		default:
			if name == "" {
				name = a
			} else if host == "" {
				host = a
			} else {
				fmt.Fprintln(os.Stderr, "usage: gitbay remote add <name> <host> [--port n] [--user u] [--ssh-option opt]... [--default]")
				return protocol.ExitUsage
			}
			i++
		}
	}
	if name == "" || host == "" {
		fmt.Fprintln(os.Stderr, "usage: gitbay remote add <name> <host> [--port n] [--user u] [--ssh-option opt]... [--default]")
		return protocol.ExitUsage
	}
	cfg, err := cliconfig.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	cfg.Instances[name] = cliconfig.Instance{Host: host, Port: port, User: user, SSHOptions: sshOptions}
	if setDefault || cfg.Default == "" {
		cfg.Default = name
	}
	if err := cliconfig.Save(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	fmt.Printf("added instance %s (%s)\n", name, host)
	return 0
}

func cmdRemoteList() int {
	cfg, err := cliconfig.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	for name, inst := range cfg.Instances {
		def := ""
		if name == cfg.Default {
			def = " (default)"
		}
		port := inst.Port
		if port == 0 {
			port = 22
		}
		fmt.Printf("%s\t%s@%s:%d%s\n", name, inst.SSHUser(), inst.Host, port, def)
	}
	return 0
}

// currentBranch is the checked-out branch of the working directory's
// clone, or "" outside a clone or on a detached HEAD.
func currentBranch() string {
	out, err := exec.Command("git", "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}
