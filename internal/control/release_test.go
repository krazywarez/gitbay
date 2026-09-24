package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestReleaseListPagesAndHidesTagTitle(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, tag := range []string{"v1", "v2", "v3"} {
		title := tag
		if tag == "v2" {
			title = "Second"
		}
		if _, err := st.CreateRelease(repo.ID, tag, title, "", uid, "md"); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(c.Stdout.(*bytes.Buffer).String()), "\n")
	if len(lines) != 3 || lines[0] != "v3\t\t0 asset(s)" || lines[1] != "v2\tSecond\t0 asset(s)" || !strings.HasPrefix(lines[2], "next\t") {
		t.Fatalf("page 1:\n%s", strings.Join(lines, "\n"))
	}
	cursor := strings.TrimPrefix(lines[2], "next\t")

	c, errOut = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--cursor", cursor}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := c.Stdout.(*bytes.Buffer).String(); got != "v1\t\t0 asset(s)\n" {
		t.Fatalf("page 2: %q", got)
	}
}
