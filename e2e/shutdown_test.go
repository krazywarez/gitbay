package e2e

import (
	"net/http"
	"syscall"
	"testing"
	"time"
)

// SIGTERM is how a deploy restarts the daemon. It used to be a plain
// kill: no listener closed, no request or push allowed to finish. The
// daemon now stops its listeners, drains, and exits 0 (#105).
func TestServeStopsOnSIGTERM(t *testing.T) {
	inst := startInstance(t)
	if resp, err := http.Get(inst.base() + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz before shutdown: %v", err)
	}
	if err := inst.proc.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.proc.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon did not exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon still running 15s after SIGTERM")
	}
	if _, err := http.Get(inst.base() + "/healthz"); err == nil {
		t.Fatal("http listener still answering after shutdown")
	}
}
