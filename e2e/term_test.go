package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GITBAY_TERM selects terminal output per session. Stock ssh without it
// gets the plain rows scripts read.
func TestTermEnvSelectsTerminalOutput(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %d %s", code, errOut)
	}

	plain, _, _ := inst.sshTerm(t, key, "", "repo", "list")
	if strings.Contains(plain, "PATH") || !strings.Contains(plain, "alice/app\t") {
		t.Errorf("plain repo list: %q", plain)
	}
	term, _, _ := inst.sshTerm(t, key, "80,color", "repo", "list")
	if !strings.HasPrefix(term, "\x1b[2mPATH") {
		t.Errorf("terminal repo list: %q", term)
	}
}

// The CLI shares one connection per instance. Each session's terminal
// selection must reach the server on its own, not the one the master
// session was opened with — which is why it travels as a leading
// --term=<v> argument rather than SetEnv: OpenSSH's mux client does not
// forward a new session's SetEnv onto an existing ControlMaster.
func TestTermEnvOverMultiplexedSession(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")
	inst.ssh(t, key, "", "repo", "create", "alice/app")

	dir, err := os.MkdirTemp("", "gbmux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "cm")
	mux := func(term string) string {
		t.Helper()
		cmdArgs := []string{"repo", "list"}
		if term != "" {
			// "--" stops the local ssh client from parsing --term=... as one
			// of its own options; it is not part of the remote command line.
			cmdArgs = append([]string{"--", "--term=" + term}, cmdArgs...)
		}
		cmd := inst.sshCmd(key, cmdArgs...)
		opts := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + sock, "-o", "ControlPersist=30"}
		for j, a := range cmd.Args {
			if a == "git@127.0.0.1" {
				cmd.Args = append(cmd.Args[:j:j], append(opts, cmd.Args[j:]...)...)
				break
			}
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ssh %s: %v", term, err)
		}
		return string(out)
	}
	t.Cleanup(func() {
		exec.Command("ssh", "-o", "ControlPath="+sock, "-O", "exit", "git@127.0.0.1").Run()
	})

	if out := mux("80"); !strings.HasPrefix(out, "PATH") {
		t.Fatalf("master session: %q", out)
	}
	if out := mux("80,color"); !strings.HasPrefix(out, "\x1b[2mPATH") {
		t.Errorf("second session kept the master's GITBAY_TERM: %q", out)
	}
	if out := mux(""); strings.Contains(out, "PATH") {
		t.Errorf("session without GITBAY_TERM got terminal output: %q", out)
	}
}
