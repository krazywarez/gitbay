//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// buildCgroups is the runner's own cgroup subtree for builds. The unit's
// Delegate=yes hands the runner its service cgroup; the runner parks
// itself in a leaf so the service cgroup can enable controllers for
// children (a cgroup may hold processes or controller-enabled children,
// not both), and creates one child per build under builds/.
type buildCgroups struct {
	builds string // <service cgroup>/builds
}

const cgroupControllers = "+cpu +memory +pids"

// prepareBuildCgroups moves the runner into <own>/runner, enables the
// controllers on its original cgroup, and creates builds/. It fails
// where the cgroup is not writable, which is a unit without
// Delegate=yes; the caller decides whether that is fatal.
func prepareBuildCgroups() (*buildCgroups, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	own, err := ownCgroupPath(string(raw))
	if err != nil {
		return nil, err
	}
	root := filepath.Join("/sys/fs/cgroup", own)
	leaf := filepath.Join(root, "runner")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return nil, fmt.Errorf("%s is not writable; the unit needs Delegate=yes: %w", root, err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, fmt.Errorf("moving into %s: %w", leaf, err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte(cgroupControllers), 0o644); err != nil {
		return nil, fmt.Errorf("enabling controllers on %s: %w", root, err)
	}
	builds := filepath.Join(root, "builds")
	if err := os.MkdirAll(builds, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(builds, "cgroup.subtree_control"), []byte(cgroupControllers), 0o644); err != nil {
		return nil, fmt.Errorf("enabling controllers on %s: %w", builds, err)
	}
	return &buildCgroups{builds: builds}, nil
}

// create makes the cgroup for one build with its limits written, and
// returns its path and an open directory fd for placing processes.
func (c *buildCgroups) create(id int64, memory, cpus string) (string, *os.File, error) {
	dir := filepath.Join(c.builds, fmt.Sprintf("build-%d", id))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", nil, err
	}
	if err := writeLimits(dir, memory, cpus); err != nil {
		os.Remove(dir)
		return "", nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		os.Remove(dir)
		return "", nil, err
	}
	return dir, f, nil
}

// remove kills whatever is still in the build's cgroup — conmon, the
// pause process, a step's stray child — and removes it. rmdir fails
// until the kernel has reaped every process, so it retries briefly.
func (c *buildCgroups) remove(dir string) {
	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o644); err != nil {
		log.Printf("cgroup %s: kill: %v", dir, err)
	}
	for i := 0; i < 50; i++ {
		if err := os.Remove(dir); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("cgroup %s: still populated after kill; left in place", dir)
}

// intoCgroup starts cmd inside the cgroup fd refers to, so podman,
// conmon and everything they start inherit the build's limits. A podman
// exec started from the runner's own cgroup lands there, outside the
// limit, which is why every invocation for a build goes through this.
func intoCgroup(cmd *exec.Cmd, f *os.File) {
	if f == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(f.Fd())
}
