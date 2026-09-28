package e2e

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pkt frames one pkt-line.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// readPkt reads one pkt-line; a flush reads as "".
func readPkt(r *bufio.Reader) (string, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", err
	}
	size, err := strconv.ParseUint(string(n[:]), 16, 16)
	if err != nil {
		return "", err
	}
	if size == 0 {
		return "", nil
	}
	buf := make([]byte, size-4)
	_, err = io.ReadFull(r, buf)
	return string(buf), err
}

// fingerprint is the SHA256 fingerprint of a public key file.
func fingerprint(t *testing.T, pubPath string) string {
	t.Helper()
	out, err := exec.Command("ssh-keygen", "-lf", pubPath).Output()
	if err != nil {
		t.Fatalf("ssh-keygen -lf: %v", err)
	}
	return strings.Fields(string(out))[1]
}

// Removing a key cuts the connections it opened: every session
// multiplexed on a ControlMaster, and a push in flight, which moves no
// ref (#256).
func TestRemovedKeyCutsMultiplexedConnection(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := setupPublicRepo(t, inst, "alice/app")
	spare := inst.newKey(t, "spare")
	pub, err := os.ReadFile(spare + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "keys", "add"); code != 0 {
		t.Fatalf("keys add: %s", errOut)
	}

	// The control socket sits under the system temp dir with a short
	// name: t.TempDir() or %C on macOS passes the 104-byte socket path
	// limit.
	cmDir, err := os.MkdirTemp("", "cm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(cmDir) })
	muxArgs := []string{
		"-p", fmt.Sprint(inst.port),
		"-i", aliceKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(inst.sshDir, "known_hosts"),
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(cmDir, "s"),
		"-o", "ControlPersist=60",
	}
	mux := func(args ...string) *exec.Cmd {
		return exec.Command("ssh", append(append([]string{}, muxArgs...), args...)...)
	}
	t.Cleanup(func() { mux("-O", "exit", "git@127.0.0.1").Run() })

	if out, err := mux("git@127.0.0.1", "whoami").Output(); err != nil || strings.TrimSpace(string(out)) != "alice" {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("whoami over the master: %v %q %s", err, out, stderr)
	}

	// A push held open mid-pack: the ref update is sent, the pack is not.
	push := mux("git@127.0.0.1", "git-receive-pack", "alice/app")
	stdin, err := push.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := push.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { push.Process.Kill() })
	adv := bufio.NewReader(stdout)
	first, err := readPkt(adv)
	if err != nil || len(first) < 40 {
		t.Fatalf("advertisement: %q %v", first, err)
	}
	oldSHA := first[:40]
	for {
		line, err := readPkt(adv)
		if err != nil {
			t.Fatalf("advertisement: %v", err)
		}
		if line == "" {
			break
		}
	}
	newSHA := strings.Repeat("1", 40)
	io.WriteString(stdin, pkt(oldSHA+" "+newSHA+" refs/heads/main\x00report-status\n")+"0000")
	// A pack header announcing one object, and no object.
	stdin.Write([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"))
	exited := make(chan error, 1)
	go func() {
		io.Copy(io.Discard, adv)
		exited <- push.Wait()
	}()

	if _, errOut, code := inst.ssh(t, spare, "", "keys", "remove", fingerprint(t, aliceKey+".pub")); code != 0 {
		t.Fatalf("keys remove: %s", errOut)
	}
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("the push exited cleanly after its key was removed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the push outlived its key")
	}

	// The master went with the connection; a new one authenticates
	// again, and the key is unknown.
	if out, err := mux("git@127.0.0.1", "whoami").CombinedOutput(); err == nil {
		t.Fatalf("whoami after removal succeeded: %s", out)
	}
	refs := mustGit(t, t.TempDir(), inst.gitEnv(spare), "ls-remote", inst.sshURL("alice/app"), "refs/heads/main")
	if !strings.HasPrefix(refs, oldSHA) {
		t.Fatalf("main moved: %s, want %s", refs, oldSHA)
	}
}
