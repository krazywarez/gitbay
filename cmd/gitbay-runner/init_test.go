package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// init creates the key and config once, prints the key and the attach
// command, and running it again changes nothing.
func TestInitWritesKeyAndConfigOnce(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	var out bytes.Buffer
	initOut = &out
	defer func() { initOut = os.Stdout }()

	if code := runInit([]string{"-remote", "git@example.test"}); code != 0 {
		t.Fatalf("init: exit %d\n%s", code, out.String())
	}
	cdir := filepath.Join(dir, "gitbay-runner")
	key := filepath.Join(cdir, "id_ed25519")
	pub, err := os.ReadFile(key + ".pub")
	if err != nil || !strings.HasPrefix(string(pub), "ssh-ed25519 ") {
		t.Fatalf("public key: %v %q", err, pub)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %o", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(cdir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode %o", fi.Mode().Perm())
	}
	cfg, _ := os.ReadFile(filepath.Join(cdir, "config.toml"))
	for _, want := range []string{"remote = \"git@example.test\"", "isolation = \"none\"", "untrusted = false", "identity = \"" + key + "\""} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config lacks %q:\n%s", want, cfg)
		}
	}
	for _, want := range []string{strings.TrimSpace(string(pub)), "gitbay repo runner add owner/name < " + key + ".pub", "https://example.test/owner/name/settings"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := runInit([]string{"-remote", "git@other.test"}); code != 0 {
		t.Fatalf("second init: exit %d\n%s", code, out.String())
	}
	if pub2, _ := os.ReadFile(key + ".pub"); string(pub2) != string(pub) {
		t.Fatal("second init replaced the key")
	}
	if cfg2, _ := os.ReadFile(filepath.Join(cdir, "config.toml")); string(cfg2) != string(cfg) {
		t.Fatal("second init rewrote the config")
	}
}

// podman needs an image; init refuses to write a config the runner would
// refuse to start with.
func TestInitPodmanNeedsImage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	initOut = &out
	defer func() { initOut = os.Stdout }()
	if code := runInit([]string{"-isolation", "podman"}); code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out.String())
	}
}
