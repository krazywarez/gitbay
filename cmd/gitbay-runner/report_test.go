package main

import (
	"errors"
	"os/exec"
	"testing"
	"time"
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
