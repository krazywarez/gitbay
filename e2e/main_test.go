package e2e

import (
	"flag"
	"fmt"
	"os"
	"testing"
	"time"
)

// This suite drives real git, ssh, sshd and gpg, and takes six to fifteen
// minutes depending on the machine — past `go test`'s ten-minute default.
// Exceeding it panics with the name of whichever test happened to be
// running, which is never the one at fault and reads like a hang (#143).
//
// make test passes -timeout 30m and .gitbay/ci.yml passes -timeout 20m.
// A bare `go test ./...` gets the default, so say so up front rather than
// eleven minutes later.
const minTimeout = 15 * time.Minute

func TestMain(m *testing.M) {
	flag.Parse()
	// Only for a whole-package run: `-run TestOneThing -timeout 2m` is a
	// reasonable thing to type and none of this applies to it.
	if run := flag.Lookup("test.run"); run == nil || run.Value.String() == "" {
		if f := flag.Lookup("test.timeout"); f != nil {
			d, err := time.ParseDuration(f.Value.String())
			if err == nil && d > 0 && d < minTimeout {
				fmt.Fprintf(os.Stderr,
					"e2e: -timeout is %s; this suite needs about %s.\n"+
						"Run `make test` (which passes -timeout 30m), or pass -timeout yourself.\n"+
						"Without this check the run panics part way through and blames whichever\n"+
						"test was executing at the deadline.\n", d, minTimeout)
				os.Exit(1)
			}
		}
	}
	os.Exit(m.Run())
}
