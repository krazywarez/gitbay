// Package toolpath resolves the external programs gitbay runs — git, ssh,
// sh — to absolute paths once, when the process starts, instead of
// letting the kernel search PATH on every spawn.
//
// Three things it buys, in the order they matter.
//
// A missing tool becomes one legible failure at start-up rather than an
// opaque one at the first push, on whichever request happened to need it.
//
// The command a long-lived daemon runs is then fixed for its lifetime,
// decided from the environment it was started with rather than resolved
// afresh each time. gitbayd and gitbay-runner both run under systemd with
// a root-owned PATH and a read-only /usr, so a search was never
// attacker-influenced in a shipped configuration — but a spawn that
// cannot be redirected is one fewer thing to reason about, and it is what
// the scanner asks for (go:S4036).
//
// And the lookup leaves the hot path: a repository page can spawn several
// git processes, and each was searching PATH from scratch.
package toolpath

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

var (
	mu      sync.Mutex
	cache   = map[string]string{}
	missing []string
)

// Look returns the absolute path to name, or name itself when it is not
// on PATH. Returning the bare name keeps the failure where it already
// was — exec reporting it — for anything that runs before Verify, and for
// a tool a particular binary never actually uses.
func Look(name string) string {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := cache[name]; ok {
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		p = name
		missing = append(missing, name)
	}
	cache[name] = p
	return p
}

// Verify reports the tools that were asked for and not found, so a daemon
// can refuse to start rather than fail on its first request. Call it after
// the packages that need tools are initialised.
func Verify() error {
	mu.Lock()
	defer mu.Unlock()
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("not found on PATH: %s", strings.Join(missing, ", "))
}
