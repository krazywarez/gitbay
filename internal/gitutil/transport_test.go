package gitutil

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Closing cancel kills the transport; it does not wait for the client
// to hang up. Stdin ends only after the kill, as a cut connection's
// does, so a clean exit here would mean the kill never happened.
func TestTransportCancelKillsGit(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	in, w := io.Pipe()
	cancel := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- Transport("git-upload-pack", dir, in, io.Discard, io.Discard, nil, 0, 0, cancel) }()
	close(cancel)
	time.AfterFunc(500*time.Millisecond, func() { w.Close() })
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("Transport returned %v, want the process killed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after cancel")
	}
}
