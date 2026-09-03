package e2e

import (
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGTERM is how a deploy restarts the daemon. It used to be a plain
// kill: no listener closed, no request or push allowed to finish. The
// daemon now stops its listeners, drains, and exits 0 (#105).
func TestServeStopsOnSIGTERM(t *testing.T) {
	inst := startInstance(t)
	if resp, err := http.Get(inst.base() + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz before shutdown: %v", err)
	}
	if err := inst.proc.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.proc.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon did not exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon still running 15s after SIGTERM")
	}
	if _, err := http.Get(inst.base() + "/healthz"); err == nil {
		t.Fatal("http listener still answering after shutdown")
	}
}

// A CLI keeps a shared connection open between commands. On shutdown that
// idle connection is closed at once rather than holding the drain for its
// full 30 s; only a session mid-command is waited for (#141).
func TestShutdownClosesIdleConnections(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	// A control master with no session: -N, backgrounded, persistent.
	// A unix socket path has a short limit; t.TempDir is far past it here.
	sock := fmt.Sprintf("/tmp/gitbay-e2e-cm-%d", inst.port)
	base := []string{"-p", fmt.Sprint(inst.port), "-i", aliceKey, "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=" + filepath.Join(inst.sshDir, "known_hosts"),
		"-o", "BatchMode=yes", "-o", "ControlPath=" + sock}
	master := exec.Command("ssh", append(append([]string{}, base...), "-o", "ControlMaster=yes", "-o", "ControlPersist=yes", "-N", "-f", "git@127.0.0.1")...)
	if out, err := master.CombinedOutput(); err != nil {
		t.Fatalf("control master: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("ssh", append(append([]string{}, base...), "-O", "exit", "git@127.0.0.1")...).Run()
	})
	// The shared connection works.
	if out, err := exec.Command("ssh", append(append([]string{}, base...), "git@127.0.0.1", "whoami")...).Output(); err != nil || strings.TrimSpace(string(out)) != "alice" {
		t.Fatalf("multiplexed whoami: %v %q", err, out)
	}

	start := time.Now()
	if err := inst.proc.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.proc.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon did not exit cleanly: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("daemon still running 20s after SIGTERM with only an idle connection open")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("shutdown took %s with only an idle connection open", took)
	}
}
