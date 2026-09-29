package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Podman's --memory and --cpus never applied under rootless cgroupfs: the
// container ran in the service's own cgroup and crun could not create a
// child (#188). The runner now owns the build cgroups itself and places
// podman inside one, so the limits are written by the runner in cgroup
// v2's own units.
func TestMemoryBytes(t *testing.T) {
	cases := map[string]int64{
		"64m":  64 << 20,
		"6g":   6 << 30,
		"512k": 512 << 10,
		"100b": 100,
		"4096": 4096,
		"1G":   1 << 30,
	}
	for in, want := range cases {
		got, err := memoryBytes(in)
		if err != nil || got != want {
			t.Errorf("memoryBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "6gb", "-1g", "1.5g"} {
		if _, err := memoryBytes(bad); err == nil {
			t.Errorf("memoryBytes(%q) accepted", bad)
		}
	}
}

func TestCPUMax(t *testing.T) {
	cases := map[string]string{
		"3":    "300000 100000",
		"1":    "100000 100000",
		"1.5":  "150000 100000",
		"0.25": "25000 100000",
	}
	for in, want := range cases {
		got, err := cpuMax(in)
		if err != nil || got != want {
			t.Errorf("cpuMax(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-1", "two"} {
		if _, err := cpuMax(bad); err == nil {
			t.Errorf("cpuMax(%q) accepted", bad)
		}
	}
}

// /proc/self/cgroup on cgroup v2 is one line, "0::<path>".
func TestOwnCgroupPath(t *testing.T) {
	got, err := ownCgroupPath("0::/system.slice/gitbay-runner.service\n")
	if err != nil || got != "/system.slice/gitbay-runner.service" {
		t.Fatalf("ownCgroupPath = %q, %v", got, err)
	}
	// A v1 hierarchy, or anything else, is not something the runner
	// manages.
	if _, err := ownCgroupPath("12:memory:/user.slice\n0::/init.scope\n"); err == nil {
		t.Fatal("v1 hierarchy accepted")
	}
}

// Limits land as cgroup v2 interface files in the build's directory;
// an unset limit writes nothing, which means "inherit", not "max".
func TestWriteLimits(t *testing.T) {
	dir := t.TempDir()
	if err := writeLimits(dir, "6g", "3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "memory.max")); string(got) != "6442450944" {
		t.Errorf("memory.max = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != "300000 100000" {
		t.Errorf("cpu.max = %q", got)
	}
	empty := t.TempDir()
	if err := writeLimits(empty, "", ""); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("unset limits wrote %v", entries)
	}
	if err := writeLimits(t.TempDir(), "lots", ""); err == nil {
		t.Error("a bad memory limit was accepted")
	}
}

// A build's cgroup sits under its trust class, which is what the builds
// nftables table matches on (#260).
func TestBuildCgroupDir(t *testing.T) {
	builds := "/sys/fs/cgroup/system.slice/gitbay-runner.service/builds"
	if got := buildCgroupDir(builds, 7, true); got != builds+"/trusted/build-7" {
		t.Errorf("trusted: %s", got)
	}
	if got := buildCgroupDir(builds, 8, false); got != builds+"/untrusted/build-8" {
		t.Errorf("untrusted: %s", got)
	}
}

// The builds table and the drop-in that creates its cgroups and loads it
// name the same class cgroups the runner places builds in. A rename on
// one side alone would leave builds unmatched, with only the uid table
// between them and the host.
func TestBuildsTableNamesTheClassCgroups(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const unit = "system.slice/gitbay-runner.service"
	table, dropin := read("gitbay-runner-builds.nft"), read("gitbay-runner.override.conf")
	for _, class := range buildClasses {
		match := fmt.Sprintf(`socket cgroupv2 level 4 "%s/builds/%s" jump %s`, unit, class, class)
		if !strings.Contains(table, match) {
			t.Errorf("gitbay-runner-builds.nft lacks %q", match)
		}
		dir := "/sys/fs/cgroup/" + unit + "/builds/" + class
		if !strings.Contains(dropin, dir) {
			t.Errorf("the drop-in does not create %s", dir)
		}
	}
	if !strings.Contains(dropin, "ExecStartPre=+/usr/sbin/nft -f /etc/gitbay-runner/builds.nft") {
		t.Error("the drop-in does not load the builds table")
	}
}

// Without build cgroups a podman runner may carry on only where nothing
// depends on them: no limits, no untrusted builds, and not on the
// daemon's host, where the builds table matches by cgroup (#260).
func TestBuildCgroupsRequired(t *testing.T) {
	cases := []struct {
		memory, cpus        string
		untrusted, loopback bool
		want                string
	}{
		{"", "", false, false, ""},
		{"6g", "", false, false, "-memory/-cpus"},
		{"", "3", false, false, "-memory/-cpus"},
		{"", "", true, false, "-untrusted"},
		{"", "", false, true, "a loopback -remote"},
		{"", "", true, true, "-untrusted"},
	}
	for _, c := range cases {
		if got := buildCgroupsRequired(c.memory, c.cpus, c.untrusted, c.loopback); got != c.want {
			t.Errorf("buildCgroupsRequired(%q, %q, %v, %v) = %q, want %q", c.memory, c.cpus, c.untrusted, c.loopback, got, c.want)
		}
	}
}
