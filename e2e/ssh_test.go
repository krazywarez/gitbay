// Package e2e drives a real gitbayd with the real ssh and git clients.
package e2e

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type instance struct {
	gitbayd  string // path to built binary
	runner   string // path to built gitbay-runner (CI tests)
	root     string
	config   string
	port     int
	httpPort int
	gitPort  int
	proc     *exec.Cmd
	sshDir   string // per-user client keys live here
}

// nextPort hands out candidate ports. Seeded randomly so two test processes
// on one machine — `go test ./...` runs packages concurrently — start in
// different places.
var nextPort = func() *atomic.Int32 {
	var n atomic.Int32
	n.Store(int32(20000 + rand.IntN(20000)))
	return &n
}()

// freePorts reserves n distinct ports, counting up rather than asking the
// kernel for :0.
//
// :0 cannot be made safe once tests run in parallel. A port is picked by
// binding, reading the number back and closing, and between that close and
// the bind inside gitbayd the kernel is free to hand the same port to
// another instance picking at that moment. The loser does not fail
// cleanly: waitForPort only asks whether something is listening, so a test
// whose port was taken talks to a different test's server and reports
// whatever that one says.
//
// A counter cannot collide within a process, whatever the interleaving.
// Each candidate is still bind-tested, which skips ports other programs
// hold. An outside process taking one in the close-to-bind window remains
// possible, as it was before; that race is not ours to close.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ports := make([]int, 0, n)
	for len(ports) < n {
		p := int(nextPort.Add(1))
		if p > 60000 {
			t.Fatal("ran out of ports")
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // somebody else has it
		}
		ln.Close()
		ports = append(ports, p)
	}
	return ports
}

func freePort(t *testing.T) int {
	t.Helper()
	return freePorts(t, 1)[0]
}

func startInstance(t *testing.T) *instance {
	return startInstanceWith(t, "")
}

// startInstanceWith appends extra TOML to the instance config.
func startInstanceWith(t *testing.T, extra string) *instance {
	t.Helper()
	ports := freePorts(t, 3)
	inst := &instance{
		gitbayd:  buildGitbayd(t),
		root:     t.TempDir(),
		port:     ports[0],
		httpPort: ports[1],
		gitPort:  ports[2],
		sshDir:   t.TempDir(),
	}
	inst.config = filepath.Join(inst.root, "config.toml")
	cfg := fmt.Sprintf(`
[server]
root = %q
site_url = "https://gitbay.test"
[ssh]
port = %d
[http]
addr = "127.0.0.1:%d"
tls = "off"
[git_daemon]
enabled = true
port = %d
`, inst.root, inst.port, inst.httpPort, inst.gitPort)
	cfg += extra + "\n"
	if err := os.WriteFile(inst.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		inst.proc.Process.Kill()
		inst.proc.Wait()
	})

	// Every listener, not just SSH: the HTTP and git ones come up in their
	// own goroutines, and a test whose first act is an HTTP request used
	// to race them and be refused.
	for _, port := range []int{inst.port, inst.httpPort, inst.gitPort} {
		waitForPort(t, port)
	}
	return inst
}

// admin runs a gitbayd admin command against the instance's database.
func (i *instance) admin(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(i.gitbayd, append([]string{"--config", i.config}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gitbayd %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// forgedAdminErr runs an admin command expected to fail, returning output.
func (i *instance) forgedAdminErr(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(i.gitbayd, append([]string{"--config", i.config}, args...)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gitbayd %v unexpectedly succeeded:\n%s", args, out)
	}
	return string(out)
}

// newKey generates a client keypair and returns the private key path.
func (i *instance) newKey(t *testing.T, name string) string {
	t.Helper()
	priv := filepath.Join(i.sshDir, name)
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", priv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	return priv
}

// sshCmd is the ssh invocation ssh runs, for a test that reads the output
// as it arrives.
func (i *instance) sshCmd(key string, args ...string) *exec.Cmd {
	base := []string{
		"-p", fmt.Sprint(i.port),
		"-i", key,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(i.sshDir, "known_hosts"),
		"-o", "BatchMode=yes",
		"git@127.0.0.1",
	}
	return exec.Command("ssh", append(base, args...)...)
}

// ssh runs the real OpenSSH client against the instance with the given key.
func (i *instance) ssh(t *testing.T, key string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := i.sshCmd(key, args...)
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

func TestControlPlaneOverBareSSH(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// whoami --json from bare OpenSSH.
	out, errOut, code := inst.ssh(t, aliceKey, "", "whoami", "--json")
	if code != 0 {
		t.Fatalf("whoami exit %d, stderr: %s", code, errOut)
	}
	var env struct {
		ProtocolVersion int `json:"protocol_version"`
		Data            struct {
			Username string `json:"username"`
			KeyScope string `json:"key_scope"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("whoami output not JSON: %v\n%s", err, out)
	}
	if env.Data.Username != "alice" || env.ProtocolVersion != 1 || env.Data.KeyScope != "full" {
		t.Fatalf("whoami = %+v", env)
	}

	// Unknown key is refused at auth.
	strangerKey := inst.newKey(t, "stranger")
	_, _, code = inst.ssh(t, strangerKey, "", "whoami")
	if code == 0 {
		t.Fatal("unknown key was authenticated")
	}

	// keys add over stdin, then list shows both.
	secondKey := inst.newKey(t, "alice2")
	pub, _ := os.ReadFile(secondKey + ".pub")
	out, errOut, code = inst.ssh(t, aliceKey, string(pub), "keys", "add", "--scope", "git")
	if code != 0 {
		t.Fatalf("keys add exit %d, stderr: %s", code, errOut)
	}
	out, _, code = inst.ssh(t, aliceKey, "", "keys", "list")
	if code != 0 || len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Fatalf("keys list exit %d:\n%s", code, out)
	}
	// The key's comment (ssh-keygen -C) is its label; keys label renames it.
	if !strings.Contains(out, "\tgit\talice2\n") {
		t.Fatalf("keys list lacks the comment as label:\n%s", out)
	}
	secondFP := strings.Fields(strings.Split(strings.TrimSpace(out), "\n")[1])[0]
	if _, errOut, code := inst.ssh(t, aliceKey, "", "keys", "label", secondFP, "'build box'"); code != 0 {
		t.Fatalf("keys label exit %d, stderr: %s", code, errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "keys", "list")
	if !strings.Contains(out, "\tgit\tbuild box\n") {
		t.Fatalf("keys list after label:\n%s", out)
	}

	// The git-scoped key authenticates but is denied control commands.
	out, errOut, code = inst.ssh(t, secondKey, "", "whoami")
	if code != 4 {
		t.Fatalf("git-scoped whoami: exit %d (want 4), stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "does not allow control commands") {
		t.Fatalf("scope denial message missing: %q", errOut)
	}

	// Duplicate key registration: bob cannot claim alice's key, and the
	// message is the exact spec text, naming no account.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	alicePub, _ := os.ReadFile(aliceKey + ".pub")
	_, errOut, code = inst.ssh(t, bobKey, string(alicePub), "keys", "add")
	if code != 1 {
		t.Fatalf("duplicate key add: exit %d, want 1", code)
	}
	want := "that key is already registered to another account; remove it there first or use a different key"
	if !strings.Contains(errOut, want) {
		t.Fatalf("duplicate key message = %q, want %q", errOut, want)
	}
	if strings.Contains(errOut, "alice") {
		t.Fatalf("duplicate key message leaks account name: %q", errOut)
	}

	// Arguments with spaces survive the tokenizer round trip.
	_, errOut, code = inst.ssh(t, aliceKey, "", "keys", "remove", "'no such fingerprint'")
	if code != 3 {
		t.Fatalf("keys remove with spaced arg: exit %d (want 3), stderr %q", code, errOut)
	}
}

// freePort used to close its listener before returning, so the kernel was
// free to hand the same port to the next call. An instance asks for three in
// a row and then fails to bind its second listener, which surfaces as an
// unrelated test timing out on "gitbayd did not start listening".
func TestFreePortsAreDistinct(t *testing.T) {
	t.Parallel()
	for round := 0; round < 50; round++ {
		seen := map[int]bool{}
		for _, p := range freePorts(t, 8) {
			if seen[p] {
				t.Fatalf("round %d: port %d issued twice in one request", round, p)
			}
			seen[p] = true
		}
	}
}
