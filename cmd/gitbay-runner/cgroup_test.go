package main

import (
	"os"
	"path/filepath"
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
