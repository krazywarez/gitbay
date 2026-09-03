package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/cliconfig"
)

// Every ssh the CLI spawns shares one connection per instance, so a
// command costs a round trip rather than a handshake (#94). The socket
// sits under ~/.ssh; with no such directory, or no_multiplex set, the
// arguments are the plain ones.
func TestSSHArgsMultiplex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	inst := cliconfig.Instance{Host: "forge.test", Port: 2222, SSHOptions: []string{"-i", "k"}}

	if args := sshArgs(inst); slices.Contains(args, "ControlMaster=auto") {
		t.Errorf("multiplexing without ~/.ssh: %v", args)
	}
	os.Mkdir(filepath.Join(home, ".ssh"), 0o700)

	args := sshArgs(inst)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p 2222", "ControlMaster=auto", "ControlPersist=300",
		"ControlPath=" + filepath.Join(home, ".ssh", "gitbay-%C")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if args[len(args)-2] != "-i" || args[len(args)-1] != "k" {
		t.Errorf("profile options are not last: %v", args)
	}

	inst.NoMultiplex = true
	if args := sshArgs(inst); slices.Contains(args, "ControlMaster=auto") {
		t.Errorf("no_multiplex ignored: %v", args)
	}
}
