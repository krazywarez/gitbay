package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI sends its own path where it differs from the registered one, so
// usage and help print a command that exists: gitbay auth keys remove,
// never gitbay keys remove (#267). Stock ssh sends none and sees the
// registered path, the only one it can type.
func TestCLIUsagePrintsTheInvokingPath(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")

	c := &cli{bin: buildGitbayCLI(t), configDir: t.TempDir(), inst: inst, key: key}
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", key,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")

	_, errOut, code := c.run(t, "", "", "auth", "keys", "remove")
	if code == 0 || !strings.Contains(errOut, "usage: gitbay auth keys remove <fingerprint>") {
		t.Errorf("CLI refusal: exit %d, stderr %q", code, errOut)
	}

	out, errOut, code := c.run(t, "", "", "auth", "keys", "remove", "--help")
	if code != 0 || !strings.Contains(out, "gitbay auth keys remove <fingerprint>") {
		t.Errorf("CLI help: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	// A command whose CLI path matches sends no --path=, and off a
	// terminal prints the ssh form as before.
	_, errOut, code = c.run(t, "", "", "issue", "show", "alice/app")
	if code == 0 || !strings.Contains(errOut, "usage: ssh git@") || !strings.Contains(errOut, " issue show <owner/name> <n>") {
		t.Errorf("matching command: exit %d, stderr %q", code, errOut)
	}

	_, errOut, code = inst.ssh(t, key, "", "keys", "remove")
	if code == 0 || !strings.Contains(errOut, "usage: ssh git@") || !strings.Contains(errOut, " keys remove <fingerprint>") {
		t.Errorf("stock ssh: exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(errOut, "auth") {
		t.Errorf("stock ssh saw the CLI's auth grouping: %q", errOut)
	}

	// A bare `gitbay auth --help` (cliPath == "auth", the registered
	// prefix it asks for) must still send --path=auth: the registry has
	// no "auth" command, so withCLIPath's equality shortcut would
	// otherwise read that as "no override needed" and print the
	// registered rows the CLI cannot actually type (#267 review finding).
	out, errOut, code = c.run(t, "", "", "auth", "--help")
	if code != 0 {
		t.Errorf("gitbay auth --help: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, want := range []string{"auth keys add", "auth export"} {
		if !strings.Contains(out, want) {
			t.Errorf("gitbay auth --help: missing %q in %q", want, out)
		}
	}

	out, errOut, code = inst.ssh(t, key, "", "help", "auth")
	if code != 0 {
		t.Errorf("ssh help auth: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, want := range []string{"keys add", "account export"} {
		if !strings.Contains(out, want) {
			t.Errorf("ssh help auth: missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "auth keys add") {
		t.Errorf("stock ssh saw the CLI's auth grouping: %q", out)
	}
}

// keys add and pgp add wire stdin directly to the server rather than going
// through pass(), so they lost the --help check every other passthrough
// command has: --help was treated as key material instead of showing help
// (#267).
func TestKeysAddAndPGPAddCheckHelpBeforeStdin(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")

	c := &cli{bin: buildGitbayCLI(t), configDir: t.TempDir(), inst: inst, key: key}
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", key,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")

	for _, args := range [][]string{
		{"auth", "keys", "add", "--help"},
		{"auth", "pgp", "add", "--help"},
	} {
		out, errOut, code := c.run(t, "", "", args...)
		if code != 0 {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
		want := "gitbay " + strings.Join(args[:len(args)-1], " ")
		if !strings.Contains(out, want) {
			t.Errorf("%v: stdout %q does not contain %q", args, out, want)
		}
	}
}
