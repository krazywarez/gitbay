package control

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// web theme show reports system for a new account, set refuses anything
// but the three schemes, and a set value is what show reports next.
func TestWebTheme(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	var buf bytes.Buffer
	c := &Ctx{User: store.User{ID: uid, Username: "alice"}, Store: st, Stdout: &buf, Stderr: io.Discard, JSON: true}

	if code := runWebThemeShow(c, nil); code != protocol.ExitOK || !strings.Contains(buf.String(), `"theme":"system"`) {
		t.Fatalf("show: %d %s", code, buf.String())
	}
	if code := runWebThemeSet(c, []string{"blue"}); code != protocol.ExitUsage {
		t.Fatalf("bad value exited %d", code)
	}
	if code := runWebThemeSet(c, nil); code != protocol.ExitUsage {
		t.Fatalf("no value exited %d", code)
	}
	buf.Reset()
	if code := runWebThemeSet(c, []string{"dark"}); code != protocol.ExitOK || !strings.Contains(buf.String(), `"theme":"dark"`) {
		t.Fatalf("set: %d %s", code, buf.String())
	}
	if got, _ := st.Theme(uid); got != "dark" {
		t.Fatalf("stored theme: %q", got)
	}
}
