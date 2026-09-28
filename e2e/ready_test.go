package e2e

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// startupWait bounds how long a gitbayd start may take before its
// listeners answer. CI runs these tests in parallel on a four-core runner
// beside other builds, where a start has taken longer than ten seconds;
// the wait ends early if the daemon exits.
const startupWait = 60 * time.Second

// tailBuffer keeps the last max bytes written to it, for a failure message.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.TrimSpace(b.buf))
}

// exited reports whether the process has ended without reaping it, so the
// test can still Wait on it. It reads /proc on Linux, where CI runs, and
// reports false elsewhere.
func exited(pid int) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// The state follows the command name, which is in parentheses.
	s := string(stat)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] == 'Z'
	}
	return false
}

func dialable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitListening waits until every port accepts a connection. It fails at
// once if the instance's daemon exits, and on either failure names the
// ports that never answered and shows the end of the daemon's stderr.
func (i *instance) waitListening(t *testing.T, ports ...int) {
	t.Helper()
	deadline := time.Now().Add(startupWait)
	pending := ports
	for {
		var still []int
		for _, p := range pending {
			if !dialable(p) {
				still = append(still, p)
			}
		}
		if len(still) == 0 {
			return
		}
		pending = still
		why := ""
		switch {
		case i.proc != nil && i.proc.Process != nil && exited(i.proc.Process.Pid):
			why = "gitbayd exited"
		case time.Now().After(deadline):
			why = fmt.Sprintf("gitbayd still not listening after %s", startupWait)
		}
		if why != "" {
			log := "(not captured)"
			if i.stderr != nil {
				log = i.stderr.String()
			}
			t.Fatalf("%s on %v; its stderr ends:\n%s", why, pending, log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
