// Package e2e drives a real gitbayd with the real ssh and git clients.
package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func buildGitbayd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gitbayd")
	cmd := exec.Command("go", "build", "-o", bin, "gitbay.org/gitbay/cmd/gitbayd")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gitbayd: %v\n%s", err, out)
	}
	return bin
}

// freePorts reserves n distinct ports. A port is chosen by binding :0 and
// reading back what the kernel assigned, so every listener has to stay open
// until all of them are picked — closing one before picking the next lets
// the kernel hand out the same port again, and the instance that asked for
// three then fails to bind its second listener.
//
// Still a narrowing rather than a guarantee: another process can take a port
// between the close here and the bind in gitbayd. Distinctness within one
// instance is the part that is ours.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	lns := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns = append(lns, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range lns {
		ln.Close()
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

	// Wait for the listener.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inst.port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return inst
		}
		if time.Now().After(deadline) {
			t.Fatal("gitbayd did not start listening")
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// ssh runs the real OpenSSH client against the instance with the given key.
func (i *instance) ssh(t *testing.T, key string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	base := []string{
		"-p", fmt.Sprint(i.port),
		"-i", key,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(i.sshDir, "known_hosts"),
		"-o", "BatchMode=yes",
		"git@127.0.0.1",
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

func TestControlPlaneOverBareSSH(t *testing.T) {
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
	if code != 2 {
		t.Fatalf("duplicate key add: exit %d, want 2", code)
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
