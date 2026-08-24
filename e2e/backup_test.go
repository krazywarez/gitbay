package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminBackup(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// Content worth backing up: a repo with commits and a tag, and an issue.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/keep"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/keep"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("precious\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "keep me")
	mustGit(t, dir, env, "tag", "v1")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1")
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/keep", "--title", "'survives backup'"); code != 0 {
		t.Fatal("issue create failed")
	}

	// Back up while the daemon is running.
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	out := inst.admin(t, "admin", "backup", "--out", archive)
	if !strings.Contains(out, "1 repositories") {
		t.Fatalf("backup summary: %s", out)
	}

	// The archive holds the snapshot, the repo, and the host key — and none
	// of the transient state.
	list, err := exec.Command("tar", "-tzf", archive).Output()
	if err != nil {
		t.Fatal(err)
	}
	names := string(list)
	for _, want := range []string{"gitbay.db", "repos/alice/keep.git/", "ssh/host_ed25519"} {
		if !strings.Contains(names, want) {
			t.Fatalf("archive missing %s:\n%s", want, names)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(names), "\n") {
		// Top-level transient state must be absent; a repo's own inert
		// sample hooks directory (keep.git/hooks/) is fine.
		for _, banned := range []string{"hook.sock", "hooks/", "askpass.sh", "gitbay.db-wal"} {
			if line == banned || strings.HasPrefix(line, banned) {
				t.Fatalf("archive contains transient state %s:\n%s", line, names)
			}
		}
	}

	// Restore: extract into a fresh root and serve from it.
	root2 := t.TempDir()
	if outB, err := exec.Command("tar", "-xzf", archive, "-C", root2).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v\n%s", err, outB)
	}
	port2 := freePort(t)
	httpPort2 := freePort(t)
	config2 := filepath.Join(root2, "config.toml")
	cfg := fmt.Sprintf(`
[server]
root = %q
site_url = "https://gitbay.test"
[ssh]
port = %d
[http]
addr = "127.0.0.1:%d"
tls = "off"
`, root2, port2, httpPort2)
	if err := os.WriteFile(config2, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	proc2 := exec.Command(inst.gitbayd, "--config", config2, "serve")
	proc2.Stderr = os.Stderr
	if err := proc2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc2.Process.Kill(); proc2.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port2), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restored gitbayd did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Strict host key checking against the ORIGINAL instance's host key:
	// the preserved key means the restored server is cryptographically the
	// same host. known_hosts entries are per host:port, so rebind the
	// original entry to the new port.
	khRaw, err := os.ReadFile(filepath.Join(inst.sshDir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.SplitN(string(khRaw), "\n", 2)[0])
	if len(fields) < 3 {
		t.Fatalf("unexpected known_hosts: %q", khRaw)
	}
	kh2 := filepath.Join(t.TempDir(), "known_hosts")
	entry := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port2, fields[1], fields[2])
	if err := os.WriteFile(kh2, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh2 := func(args ...string) (string, string, int) {
		base := []string{
			"-p", fmt.Sprint(port2), "-i", aliceKey,
			"-o", "IdentitiesOnly=yes",
			"-o", "UserKnownHostsFile=" + kh2,
			"-o", "StrictHostKeyChecking=yes",
			"-o", "BatchMode=yes",
			"git@127.0.0.1",
		}
		cmd := exec.Command("ssh", append(base, args...)...)
		var o, e strings.Builder
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("ssh: %v", err)
		}
		return o.String(), e.String(), code
	}

	// Identity, repo data, and issue all survived.
	out2, errOut, code := ssh2("whoami")
	if code != 0 || strings.TrimSpace(out2) != "alice" {
		t.Fatalf("whoami on restored instance: exit %d, %q, %s", code, out2, errOut)
	}
	if out2, _, code = ssh2("repo", "log", "alice/keep"); code != 0 || !strings.Contains(out2, "keep me") {
		t.Fatalf("restored log: %d\n%s", code, out2)
	}
	if out2, _, code = ssh2("issue", "show", "alice/keep", "1"); code != 0 || !strings.Contains(out2, "survives backup") {
		t.Fatalf("restored issue: %d\n%s", code, out2)
	}

	// The restored instance accepts new pushes: hooks were regenerated at
	// startup, not restored from the archive.
	env2 := append(os.Environ(),
		fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o BatchMode=yes",
			aliceKey, kh2),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	work2 := t.TempDir()
	mustGit(t, work2, env2, "clone", fmt.Sprintf("ssh://git@127.0.0.1:%d/alice/keep.git", port2), "w")
	dir2 := filepath.Join(work2, "w")
	if data, _ := os.ReadFile(filepath.Join(dir2, "data.txt")); string(data) != "precious\n" {
		t.Fatalf("restored content: %q", data)
	}
	mustGit(t, dir2, env2, "commit", "-q", "--allow-empty", "-m", "post-restore")
	mustGit(t, dir2, env2, "push", "-q", "origin", "main")
}
