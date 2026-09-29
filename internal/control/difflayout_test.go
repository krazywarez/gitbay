package control

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// web diff show reports unified for a new account, set refuses anything
// but the two layouts, and a set value is what show reports next.
func TestWebDiffLayout(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	var buf bytes.Buffer
	c := &Ctx{User: store.User{ID: uid, Username: "alice"}, Store: st, Stdout: &buf, Stderr: io.Discard, JSON: true}

	if code := runWebDiffShow(c, nil); code != protocol.ExitOK || !strings.Contains(buf.String(), `"layout":"unified"`) {
		t.Fatalf("show: %d %s", code, buf.String())
	}
	if code := runWebDiffSet(c, []string{"triple"}); code != protocol.ExitUsage {
		t.Fatalf("bad value exited %d", code)
	}
	if code := runWebDiffSet(c, nil); code != protocol.ExitUsage {
		t.Fatalf("no value exited %d", code)
	}
	buf.Reset()
	if code := runWebDiffSet(c, []string{"split"}); code != protocol.ExitOK || !strings.Contains(buf.String(), `"layout":"split"`) {
		t.Fatalf("set: %d %s", code, buf.String())
	}
	if got, _ := st.DiffLayout(uid); got != "split" {
		t.Fatalf("stored layout: %q", got)
	}
}
