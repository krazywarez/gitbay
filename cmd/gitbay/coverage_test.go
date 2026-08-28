package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/control"
)

// Commands with no CLI front-end on purpose. Everything else in the
// registry must be reachable by typing it, or the CLI is not the
// complete interface the design claims.
var notInCLI = map[string]string{
	"help":                  "cobra provides its own",
	"runner next":           "the CI runner's wire protocol, not for humans",
	"runner done":           "the CI runner's wire protocol, not for humans",
	"runner log":            "the CI runner's wire protocol, not for humans",
	"account import-bundle": "the wire format behind `gitbay migrate`",
}

// TestEveryCommandIsReachable guards the invariant that adding a command
// to the registry is enough. The tree is hand-written, so a command can
// land server-side — reachable over raw SSH, the web and the API — while
// `gitbay <cmd>` still says "unknown command". That happened to `explore`
// and `wiki`.
func TestEveryCommandIsReachable(t *testing.T) {
	wired := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if p := c.Annotations[serverPath]; p != "" {
			wired[p] = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())

	for _, cmd := range control.Commands() {
		path := strings.Join(cmd.Path, " ")
		if wired[path] || notInCLI[path] != "" {
			continue
		}
		t.Errorf("%q is in the registry but not in the CLI tree; "+
			"add a pass() for it, or list it in notInCLI with a reason", path)
	}

	// A hand-built command (auth pgp add, repo settings visibility) is
	// fine, but it must still name a real command.
	for path := range wired {
		if _, _, ok := control.Lookup(strings.Fields(path)); !ok {
			t.Errorf("CLI dispatches %q, which the registry does not define", path)
		}
	}
}
