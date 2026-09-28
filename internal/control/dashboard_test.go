package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestDashboardEmptySectionsSayNone(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"dashboard"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()
	for _, h := range []string{"waiting on your review:", "assigned to you:", "open merge requests:", "open issues:"} {
		if !strings.Contains(out, h+"\n  none\n") {
			t.Errorf("%q not followed by none:\n%s", h, out)
		}
	}
}

func TestDashboardActivityIsASentence(t *testing.T) {
	c := notifTestCtx(t, "cmc")
	repoID, err := c.Store.CreateRepo("user", c.User.ID, "gitbay", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.Store.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.labeled", `{"number":262,"labels":["ops","security"]}`)

	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if code := runDashboard(c, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), `{"number"`) {
		t.Errorf("raw payload leaked into plain output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "cmc labelled cmc/gitbay#262 ops, security") {
		t.Errorf("no sentence in output:\n%s", out.String())
	}
}

func TestFeedIsASentence(t *testing.T) {
	c := notifTestCtx(t, "cmc")
	repoID, err := c.Store.CreateRepo("user", c.User.ID, "gitbay", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.Store.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "issue.created", `{"number":1}`)

	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if code := runFeed(c, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "cmc opened issue cmc/gitbay#1") {
		t.Errorf("no sentence in output:\n%s", out.String())
	}
}

// The push queue is the one worker queue whose worst failure — a key_id
// or team_id Apple did not issue, which config validation cannot check —
// dead-letters every row on its first attempt with nothing but a log
// line. dashboard is where an admin would see that, so it reports push
// beside the other five queues.
func TestDashboardReportsThePushQueue(t *testing.T) {
	c := notifTestCtx(t, "cmc")
	c.User.IsAdmin = true
	uid := c.User.ID
	if _, err := c.Store.AddPushDevice(uid, "tok-a", "iphone"); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.EnqueuePush(uid, "krz/gitbay", "cmc opened issue #1", "krz/gitbay/issues/1"); err != nil {
		t.Fatal(err)
	}
	due, err := c.Store.DuePush(20)
	if err != nil || len(due) != 1 {
		t.Fatalf("DuePush: %v %+v", err, due)
	}
	if err := c.Store.MarkPushFailed(due[0].ID, "apns 403 InvalidProviderToken", nil); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if code := runDashboard(c, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "push\tpending 0\tretrying 0\tfailed 1") {
		t.Fatalf("no push queue row:\n%s", got)
	}
	if !strings.Contains(got, "apns 403 InvalidProviderToken") {
		t.Fatalf("dead-lettered row not listed:\n%s", got)
	}

	out.Reset()
	c.JSON = true
	if code := runDashboard(c, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"push":{`) {
		t.Fatalf("no push key in the queues object:\n%s", out.String())
	}
}
