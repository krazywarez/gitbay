package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSystemSSHMode runs the M1/M2 scenarios against a real host sshd using
// AuthorizedKeysCommand + forced command instead of the embedded listener.
func TestSystemSSHMode(t *testing.T) {
	sshdBin := "/usr/sbin/sshd"
	if _, err := os.Stat(sshdBin); err != nil {
		t.Skipf("no host sshd at %s", sshdBin)
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	// gitbayd in system mode: no embedded SSH listener; hookd + http still run.
	inst := startInstanceWith(t, "") // placeholder to reuse helpers; killed below
	inst.proc.Process.Kill()
	inst.proc.Wait()
	cfg := fmt.Sprintf(`
[server]
root = %q
site_url = "https://gitbay.test"
[ssh]
mode = "system"
[http]
addr = "127.0.0.1:%d"
tls = "off"
`, inst.root, inst.httpPort)
	if err := os.WriteFile(inst.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.proc.Process.Kill(); inst.proc.Wait() })

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	strangerKey := inst.newKey(t, "stranger")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Host sshd on a high port as the current user.
	sshdDir := t.TempDir()
	hostKey := filepath.Join(sshdDir, "host_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Fatalf("host keygen: %v\n%s", err, out)
	}
	// sshd requires the AuthorizedKeysCommand program itself to be owned by
	// root; a test-built binary is not. Use root-owned /bin/sh with a
	// wrapper script argument — only the command path is ownership-checked.
	wrapper := filepath.Join(sshdDir, "akc.sh")
	script := fmt.Sprintf("#!/bin/sh\nexec %q --config %q authorized-keys \"$1\" \"$2\"\n",
		inst.gitbayd, inst.config)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	sshdPort := freePort(t)
	sshdConf := filepath.Join(sshdDir, "sshd_config")
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile none
AuthorizedKeysCommand /bin/sh %s %%t %%k
AuthorizedKeysCommandUser %s
StrictModes no
UsePAM no
PidFile %s
LogLevel ERROR
`, sshdPort, hostKey, wrapper, me.Username, filepath.Join(sshdDir, "sshd.pid"))
	if err := os.WriteFile(sshdConf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	sshd := exec.Command(sshdBin, "-D", "-e", "-f", sshdConf)
	sshd.Stderr = os.Stderr
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sshd.Process.Kill(); sshd.Wait() })

	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sshdPort), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sshd did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// ssh helper against the host sshd (login user = current user; identity
	// still comes from the key).
	sysSSH := func(key, stdin string, args ...string) (string, string, int) {
		base := []string{
			"-p", fmt.Sprint(sshdPort),
			"-i", key,
			"-o", "IdentitiesOnly=yes",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=" + filepath.Join(sshdDir, "kh"),
			"-o", "BatchMode=yes",
			me.Username + "@127.0.0.1",
		}
		cmd := exec.Command("ssh", append(base, args...)...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out, errOut strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("ssh: %v", err)
		}
		return out.String(), errOut.String(), code
	}

	// M1: whoami over the host sshd.
	out, errOut, code := sysSSH(aliceKey, "", "whoami", "--json")
	if code != 0 {
		t.Fatalf("whoami via sshd: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, `"username":"alice"`) || !strings.Contains(out, `"protocol_version":1`) {
		t.Fatalf("whoami output: %s", out)
	}

	// Unknown key: authentication fails inside sshd (authorized-keys emits
	// nothing), before any forge code runs.
	_, _, code = sysSSH(strangerKey, "", "whoami")
	if code == 0 {
		t.Fatal("stranger authenticated via host sshd")
	}

	// Scoped key: registered with git-only scope, denied control commands.
	scopedKey := inst.newKey(t, "scoped")
	pub, _ := os.ReadFile(scopedKey + ".pub")
	if _, errOut, code := sysSSH(aliceKey, string(pub), "keys", "add", "--scope", "git"); code != 0 {
		t.Fatalf("keys add: %s", errOut)
	}
	_, errOut, code = sysSSH(scopedKey, "", "whoami")
	if code != 4 || !strings.Contains(errOut, "does not allow control commands") {
		t.Fatalf("scoped denial via sshd: exit %d, %s", code, errOut)
	}

	// M2: private repo, push, denial, protected branch — through host sshd.
	if _, errOut, code = sysSSH(aliceKey, "", "repo", "create", "alice/proj", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	sysGitEnv := func(key string) []string {
		return append(os.Environ(),
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
				key, filepath.Join(sshdDir, "kh")),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		)
	}
	urlFor := func(repo string) string {
		return fmt.Sprintf("ssh://%s@127.0.0.1:%d/%s.git", me.Username, sshdPort, repo)
	}

	work := t.TempDir()
	aliceEnv := sysGitEnv(aliceKey)
	mustGit(t, work, aliceEnv, "clone", urlFor("alice/proj"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o644)
	mustGit(t, dir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, aliceEnv, "add", ".")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "init")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	// Bob: authenticated but no access — not-found, not permission-denied.
	cloneOut, cloneCode := gitRun(t, t.TempDir(), sysGitEnv(bobKey), "clone", urlFor("alice/proj"))
	if cloneCode == 0 || !strings.Contains(cloneOut, "repository not found") {
		t.Fatalf("bob clone via sshd: %d\n%s", cloneCode, cloneOut)
	}

	// Protected branch: the hook path (forced command -> git -> pre-receive
	// -> daemon unix socket) refuses the force-push.
	if _, errOut, code = sysSSH(aliceKey, "", "repo", "settings", "protect", "alice/proj", "main"); code != 0 {
		t.Fatalf("protect: %s", errOut)
	}
	mustGit(t, dir, aliceEnv, "commit", "-q", "--amend", "-m", "rewritten")
	pushOut, pushCode := gitRun(t, dir, aliceEnv, "push", "--force", "origin", "main")
	if pushCode == 0 || !strings.Contains(pushOut, "force-push refused") {
		t.Fatalf("force-push via sshd: %d\n%s", pushCode, pushOut)
	}
}
