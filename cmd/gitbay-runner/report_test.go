package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
)

// exitErr fabricates the error ssh returns for a given exit status.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("could not fabricate exit %d: %v", code, err)
	}
	return err
}

func itoa(n int) string {
	return string(rune('0'+n/100)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

// A connection failure (ssh exit 255) is retried until it succeeds; the
// result is not lost to a server that was restarting (#179).
func TestReportRetriesConnectionFailure(t *testing.T) {
	calls := 0
	err := reportWithRetry(func() (string, error) {
		calls++
		if calls < 3 {
			return "ssh: connect to host 127.0.0.1 port 22: Connection refused", exitErr(t, 255)
		}
		return "", nil
	}, 7, []time.Duration{0, 0, 0, 0})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want success on the third attempt", err, calls)
	}
}

// The server's own refusal is an answer, not a transient: no retry.
func TestReportDoesNotRetryServerAnswer(t *testing.T) {
	calls := 0
	err := reportWithRetry(func() (string, error) {
		calls++
		return `{"error":"build 7 is not running"}`, exitErr(t, 3)
	}, 7, []time.Duration{0, 0})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one attempt and an error", err, calls)
	}
}

// Retries are bounded: after the delays are spent the error surfaces.
func TestReportGivesUp(t *testing.T) {
	calls := 0
	err := reportWithRetry(func() (string, error) {
		calls++
		return "", exitErr(t, 255)
	}, 7, []time.Duration{0, 0})
	if err == nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want three attempts then an error", err, calls)
	}
}

// The reason survives the trip: ssh joins arguments with spaces and the
// server splits the line again with POSIX rules (#266).
func TestDoneArgsNameTheFailedStep(t *testing.T) {
	got := doneArgs(7, "failure", &failure{Step: 3, Reason: "can't: exit 1"})
	argv, err := protocol.Tokenize(strings.Join(got, " "))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"runner", "done", "7", "failure", "--step", "3", "--reason", "can't: exit 1"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("server reads %q, want %q", argv, want)
	}
	if got := doneArgs(7, "success", nil); strings.Join(got, " ") != "runner done 7 success" {
		t.Fatalf("success: %q", got)
	}
	if got := doneArgs(7, "failure", &failure{Reason: "git clone: exit 128"}); strings.Contains(strings.Join(got, " "), "--step") {
		t.Fatalf("a failure before any step sent a step: %q", got)
	}
}
