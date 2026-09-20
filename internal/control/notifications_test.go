package control

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// notifTestCtx opens an in-memory store, migrates it, and creates one user
// to act as. Modeled on the store setup in snippet_test.go; this package
// has no shared testCtx helper.
func notifTestCtx(t *testing.T, username string) *Ctx {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(username, false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	return &Ctx{
		User:  store.User{ID: uid, Username: username},
		Scope: "full",
		Store: st,
		// Push enabled is the instance state the push tests assume; the
		// disabled case sets it back to false explicitly.
		Cfg:    config.Config{Push: config.Push{Enabled: true}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
	}
}

// testRepoWithWatcher returns a Ctx acting as alice, a repository she
// owns with issue #1 open on it, and bob's user id with a watch row on
// it — the shared setup for notify's recipient-widening tests.
func testRepoWithWatcher(t *testing.T) (*Ctx, store.Repo, int64) {
	t.Helper()
	c := notifTestCtx(t, "alice")
	repoID, err := c.Store.CreateRepo("user", c.User.ID, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.Store.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.CreateIssue(repo.ID, c.User.ID, "title", "", "markdown"); err != nil {
		t.Fatal(err)
	}
	bob, err := c.Store.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SetRepoWatch(repo.ID, bob, "watching"); err != nil {
		t.Fatal(err)
	}
	return c, repo, bob
}

func TestNotifyQueuesPush(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t) // alice acts, bob watches
	c.Store.AddPushDevice(bob, "tok-b", "iphone")

	notify(c, []int64{bob}, notice{repo: repo, kind: "issue",
		subject: "[alice/app] #1: title",
		action:  "opened issue #1",
		path:    "alice/app/issues/1"})

	due, err := c.Store.DuePush(20)
	if err != nil {
		t.Fatalf("DuePush: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("want one queued push, got %d", len(due))
	}
	// The push body is the inbox row's summary, so the two surfaces
	// cannot disagree about what happened.
	if due[0].Title != "alice/app" {
		t.Fatalf("title = %q", due[0].Title)
	}
	if due[0].Body != "alice opened issue #1" {
		t.Fatalf("body = %q", due[0].Body)
	}
	if due[0].Path != "alice/app/issues/1" {
		t.Fatalf("path = %q", due[0].Path)
	}
}

func TestNotifyQueuesNoPushForTheActor(t *testing.T) {
	c, repo, _ := testRepoWithWatcher(t)
	c.Store.AddPushDevice(c.User.ID, "tok-self", "iphone")

	notify(c, []int64{c.User.ID}, notice{repo: repo, kind: "issue",
		subject: "s", action: "opened issue #1", path: "alice/app/issues/1"})

	// NotifyRecipients already drops the actor; push inherits that and
	// must not find its own way around it.
	if due, _ := c.Store.DuePush(20); len(due) != 0 {
		t.Fatalf("queued a push to the actor")
	}
}

// TestNotifyQueuesNoPushWhenDisabled: on an instance with [push]
// enabled = false nothing drains the queue, and the retention sweep only
// collects rows that were sent or dead-lettered, so a row written here is
// never collected. The mail half already gates on the instance having
// SMTP; push gates the same way.
func TestNotifyQueuesNoPushWhenDisabled(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)
	c.Cfg.Push.Enabled = false
	c.Store.AddPushDevice(bob, "tok-b", "iphone")

	notify(c, []int64{bob}, notice{repo: repo, kind: "issue",
		subject: "s", action: "opened issue #1", path: "alice/app/issues/1"})

	if due, _ := c.Store.DuePush(20); len(due) != 0 {
		t.Fatalf("queued %d pushes on a push-disabled instance", len(due))
	}
	// The inbox row is still filed: push is the optional half, not the
	// notice.
	if n := c.Store.UnreadNotices(bob); n != 1 {
		t.Fatalf("unread notices = %d, want 1", n)
	}
}

// TestNotificationsDeviceAddRefusedWhenPushDisabled: registering a device
// on an instance that cannot deliver would report success and then never
// push, with notifications settings show still saying push is on.
func TestNotificationsDeviceAddRefusedWhenPushDisabled(t *testing.T) {
	c := notifTestCtx(t, "alice")
	c.Cfg.Push.Enabled = false
	c.Stdin = strings.NewReader("DEVTOKEN\n")
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out

	if code := runNotificationsDeviceAdd(c, nil); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d", code, protocol.ExitFailure)
	}
	if devices, _ := c.Store.PushDevices(c.User.ID); len(devices) != 0 {
		t.Fatalf("device registered anyway: %+v", devices)
	}
	if !strings.Contains(out.String(), "[push] enabled = false") {
		t.Fatalf("message does not name the instance setting: %q", out.String())
	}
}

func TestNotificationsDeviceAddReadsStdin(t *testing.T) {
	c := notifTestCtx(t, "alice")
	c.Stdin = strings.NewReader("DEVTOKEN\n")
	if code := runNotificationsDeviceAdd(c, []string{"--label", "iphone"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	devices, _ := c.Store.PushDevices(c.User.ID)
	if len(devices) != 1 || devices[0].Token != "DEVTOKEN" {
		t.Fatalf("got %+v", devices)
	}
	if devices[0].Label != "iphone" {
		t.Fatalf("label = %q", devices[0].Label)
	}
}

func TestNotificationsDeviceListTruncatesTheToken(t *testing.T) {
	c := notifTestCtx(t, "alice")
	long := strings.Repeat("a", 64)
	c.Store.AddPushDevice(c.User.ID, long, "iphone")
	var out bytes.Buffer
	c.Stdout = &out
	if code := runNotificationsDeviceList(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), long) {
		t.Fatal("the full token was printed")
	}
}

// TestNotificationsDeviceListTruncatesTheTokenJSON is the JSON-path twin
// of the above: the plain and JSON output share the same rows slice, but
// nothing enforces that beyond reading the code, so both paths get their
// own test of the guarantee.
func TestNotificationsDeviceListTruncatesTheTokenJSON(t *testing.T) {
	c := notifTestCtx(t, "alice")
	long := strings.Repeat("a", 64)
	c.Store.AddPushDevice(c.User.ID, long, "iphone")
	var out bytes.Buffer
	c.Stdout, c.JSON = &out, true
	if code := runNotificationsDeviceList(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), long) {
		t.Fatal("the full token was printed")
	}
}

// TestNotificationsDeviceListMasksAShortToken: a token at or under the
// truncation cut length is not returned unchanged. ShortToken's short
// path used to return the token verbatim, a full echo of anything eight
// characters or fewer; runNotificationsDeviceAdd enforces no minimum
// length, so a short token is a value the command will store.
func TestNotificationsDeviceListMasksAShortToken(t *testing.T) {
	c := notifTestCtx(t, "alice")
	short := "abc123"
	c.Store.AddPushDevice(c.User.ID, short, "iphone")
	var out bytes.Buffer
	c.Stdout = &out
	if code := runNotificationsDeviceList(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), short) {
		t.Fatal("the short token was printed verbatim")
	}
}

// device add returns the row id. Without it a client that wants to
// deregister has to list devices and match its own token against the
// truncated display value, which is identity by rendered string.
func TestNotificationsDeviceAddReturnsTheID(t *testing.T) {
	c := notifTestCtx(t, "alice")
	c.Stdin = strings.NewReader("DEVTOKEN\n")
	var out bytes.Buffer
	c.Stdout, c.JSON = &out, true
	if code := runNotificationsDeviceAdd(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	devices, _ := c.Store.PushDevices(c.User.ID)
	if len(devices) != 1 {
		t.Fatalf("want one device, got %d", len(devices))
	}
	want := fmt.Sprintf(`"id":%d`, devices[0].ID)
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output %s does not carry %s", out.String(), want)
	}
}

func TestNotificationsSettingsShowsPush(t *testing.T) {
	c := notifTestCtx(t, "alice")
	var out bytes.Buffer
	c.Stdout, c.JSON = &out, true
	if code := runNotificationsSettingsShow(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), `"push":true`) {
		t.Fatalf("no push key: %s", out.String())
	}
}
