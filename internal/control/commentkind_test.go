package control

import (
	"bytes"
	"encoding/json"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A line the server posted (a label change, a retarget) says so in
// --json; a client that offers reactions must not offer them on one, since
// issue react and mr react refuse it. A comment carries no kind, so the
// wire shape of every existing comment is unchanged.
func TestShowMarksSystemComments(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateIssue(repo.ID, uid, "t", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueComment(id, uid, "me too", "md"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueSystemComment(id, uid, "added label bug"); err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.JSON = true
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var env struct {
		Data struct {
			Comments []map[string]any `json:"comments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(c.Stdout.(*bytes.Buffer).Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	cs := env.Data.Comments
	if len(cs) != 2 {
		t.Fatalf("comments: %v", cs)
	}
	if _, ok := cs[0]["kind"]; ok {
		t.Errorf("a comment carries a kind: %v", cs[0])
	}
	if cs[1]["kind"] != "system" {
		t.Errorf("a system line is not marked: %v", cs[1])
	}
}
