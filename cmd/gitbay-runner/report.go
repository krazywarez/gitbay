package main

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// reportDone tells the server a build's outcome, retrying when the
// server cannot be reached. A finished build's result used to be dropped
// on the first failure: gitbayd restarting for a deploy at the moment the
// runner reported left the build "running" on the server for good, with
// the runner already on to the next one (#179).
//
// Only a connection-level failure is retried — ssh exits 255 for those.
// Any other exit is the server's answer, and asking again would not
// change it. Four retries over about thirty seconds outlasts a restart.
func (r *runner) reportDone(id int64, status string) error {
	return reportWithRetry(func() (string, error) {
		return r.ssh(nil, "runner", "done", fmt.Sprint(id), status)
	}, id, retryDelays)
}

var retryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

func reportWithRetry(report func() (string, error), id int64, delays []time.Duration) error {
	var out string
	var err error
	for attempt := 0; ; attempt++ {
		out, err = report()
		if err == nil {
			return nil
		}
		if !connectionFailed(err) || attempt >= len(delays) {
			return fmt.Errorf("reporting build %d: %w (%s)", id, err, out)
		}
		time.Sleep(delays[attempt])
	}
}

// connectionFailed reports whether ssh itself failed to reach the server,
// which is exit status 255, as opposed to the remote command exiting.
func connectionFailed(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 255
}
