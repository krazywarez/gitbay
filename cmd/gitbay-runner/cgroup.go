package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Build limits are cgroup v2 files the runner writes itself. Podman's
// --memory and --cpus never applied here: under rootless podman with the
// cgroupfs manager the container starts inside the service's own cgroup
// and crun cannot create a child, so the flags were accepted and ignored
// (#188). The runner owns a cgroup per build under its delegated service
// cgroup instead, writes the limits into it, and starts every podman
// process for that build from inside it with podman's own cgroup handling
// off. What follows is the portable half: parsing and the file writes.

// buildClasses are the cgroups a build is placed under, by the claim's
// trust flag. deploy/gitbay-runner-builds.nft matches a build's sockets,
// pasta's included, by these two cgroups (#260), so the names are fixed.
var buildClasses = []string{"trusted", "untrusted"}

// buildCgroupDir is build id's cgroup under the runner's builds cgroup.
func buildCgroupDir(builds string, id int64, trusted bool) string {
	class := buildClasses[1]
	if trusted {
		class = buildClasses[0]
	}
	return filepath.Join(builds, class, fmt.Sprintf("build-%d", id))
}

// buildCgroupsRequired names what makes build cgroups mandatory, or ""
// when a podman runner may run its builds in its own service cgroup.
// Limits that cannot be applied are refused, not dropped: a runner that
// accepted -memory and ran uncapped is what #188 was. A runner taking
// untrusted builds, or polling over loopback on the daemon's host, is
// the shape the builds nftables table guards, and that table matches
// builds by these cgroups; without them it matches nothing (#260).
func buildCgroupsRequired(memory, cpus string, untrusted, loopback bool) string {
	switch {
	case memory != "" || cpus != "":
		return "-memory/-cpus"
	case untrusted:
		return "-untrusted"
	case loopback:
		return "a loopback -remote"
	}
	return ""
}

// memoryBytes parses podman's memory units — a whole number with an
// optional b, k, m or g suffix — into bytes.
func memoryBytes(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty memory limit")
	}
	num, unit := s, ""
	if last := s[len(s)-1]; last < '0' || last > '9' {
		num, unit = s[:len(s)-1], strings.ToLower(s[len(s)-1:])
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("memory limit %q: want a whole number of b, k, m or g", s)
	}
	shift := map[string]uint{"": 0, "b": 0, "k": 10, "m": 20, "g": 30}
	sh, ok := shift[unit]
	if !ok {
		return 0, fmt.Errorf("memory limit %q: unit %q is not b, k, m or g", s, unit)
	}
	return n << sh, nil
}

// cpuMax renders a CPU count, whole or fractional, as cgroup v2's
// "<quota> <period>" over a 100ms period.
func cpuMax(s string) (string, error) {
	const period = 100000
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return "", fmt.Errorf("cpu limit %q: want a positive number of CPUs", s)
	}
	return fmt.Sprintf("%d %d", int64(f*period+0.5), period), nil
}

// ownCgroupPath reads the cgroup v2 path out of /proc/self/cgroup
// contents. A v1 hierarchy has more than the one "0::" line and is not
// something the runner manages.
func ownCgroupPath(procSelfCgroup string) (string, error) {
	lines := strings.Split(strings.TrimSpace(procSelfCgroup), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "0::/") {
		return "", fmt.Errorf("not a cgroup v2 host: /proc/self/cgroup is %q", strings.TrimSpace(procSelfCgroup))
	}
	return strings.TrimPrefix(lines[0], "0::"), nil
}

// writeLimits writes the requested limits into a cgroup directory. An
// unset limit writes nothing, so the build inherits whatever the unit
// allows rather than getting "max".
func writeLimits(dir, memory, cpus string) error {
	if memory != "" {
		n, err := memoryBytes(memory)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(n, 10)), 0o644); err != nil {
			return err
		}
	}
	if cpus != "" {
		v, err := cpuMax(cpus)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(v), 0o644); err != nil {
			return err
		}
	}
	return nil
}
