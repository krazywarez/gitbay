package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// repo show's mirror row must go through the same second-truncation every
// other timestamp in a view uses, not print the store's raw milliseconds.
func TestRepoShowMirrorTimeIsTruncatedToTheSecond(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.AddMirror(repo.ID, "push", "ssh://example.test/x.git", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMirrorResult(id, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("UPDATE mirrors SET last_sync = ? WHERE id = ?", "2026-09-24T15:31:50.839Z", id); err != nil {
		t.Fatal(err)
	}

	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice", IsAdmin: true})
	if code := runRepoShow(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	out := c.Stdout.(*bytes.Buffer).String()
	if strings.Contains(out, ".839Z") {
		t.Errorf("milliseconds leaked: %s", out)
	}
	if !strings.Contains(out, "2026-09-24T15:31:50Z") {
		t.Errorf("no truncated timestamp: %s", out)
	}
}
