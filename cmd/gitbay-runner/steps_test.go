package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The failing step is named by number in the log and in the outcome
// reported to the server (#266).
func TestRunStepsNamesTheFailedStep(t *testing.T) {
	r := &runner{isolation: isolationNone}
	run := func(cmd *exec.Cmd, _ time.Time) (bool, string) {
		if err := cmd.Run(); err != nil {
			return false, exitReason(err)
		}
		return true, ""
	}
	env := []string{"PATH=" + os.Getenv("PATH")}
	var log strings.Builder
	f := r.runSteps(job{Steps: []string{"true", "exit 3", "true"}}, t.TempDir(), env, &log, time.Now().Add(time.Minute), run)
	if f == nil || f.Step != 2 || f.Reason != "exit 3" {
		t.Fatalf("failure %+v, want step 2, exit 3", f)
	}
	if !strings.Contains(log.String(), "step 2/3 failed: exit 3\n") {
		t.Fatalf("log does not name the step:\n%s", log.String())
	}
	if f := r.runSteps(job{Steps: []string{"true"}}, t.TempDir(), env, &log, time.Now().Add(time.Minute), run); f != nil {
		t.Fatalf("a passing job failed: %+v", f)
	}
}
