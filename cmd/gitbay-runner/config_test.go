package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config file sets the flags' values; a flag on the command line wins.
func TestConfigFileFeedsFlagsAndFlagsOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("remote = \"git@example.test\"\npoll = \"9s\"\nuntrusted = true\nidentity = \"/k\"\njobs = 2\n"), 0o600)

	values, found, err := loadConfig(path)
	if err != nil || !found {
		t.Fatalf("loadConfig: found=%v err=%v", found, err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	remote := fs.String("remote", "git@gitbay.org", "")
	poll := fs.Duration("poll", 0, "")
	untrusted := fs.Bool("untrusted", false, "")
	identity := fs.String("identity", "", "")
	jobs := fs.Int("jobs", 1, "")
	if err := applyConfig(fs, values); err != nil {
		t.Fatal(err)
	}
	if err := fs.Parse([]string{"-poll", "3s"}); err != nil {
		t.Fatal(err)
	}
	if *remote != "git@example.test" || poll.String() != "3s" || !*untrusted || *identity != "/k" || *jobs != 2 {
		t.Fatalf("remote=%s poll=%s untrusted=%v identity=%s jobs=%d", *remote, poll, *untrusted, *identity, *jobs)
	}
	if _, found, err := loadConfig(filepath.Join(dir, "missing.toml")); found || err != nil {
		t.Fatalf("missing file: found=%v err=%v", found, err)
	}
	if _, _, err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("nonsense = \"x\"\n"), 0o600)
	if _, _, err := loadConfig(path); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "/def"},
		{[]string{"-once"}, "/def"},
		{[]string{"-config", "/a"}, "/a"},
		{[]string{"--config", "/b", "-once"}, "/b"},
		{[]string{"-config=/c"}, "/c"},
	} {
		if got := configPathFromArgs(tc.args, "/def"); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.args, got, tc.want)
		}
	}
}

func TestConfigDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if got := configDir(); got != "/x/gitbay-runner" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if got := configDir(); got != "/h/.config/gitbay-runner" {
		t.Fatalf("got %s", got)
	}
}

func TestIdentityOpts(t *testing.T) {
	if got := identityOpts(""); got != nil {
		t.Fatalf("empty identity produced %v", got)
	}
	got := strings.Join(identityOpts("/k"), " ")
	want := "-F /dev/null -i /k -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
