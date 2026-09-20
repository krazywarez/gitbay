package control

import (
	"bytes"
	"strings"
	"testing"
)

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
