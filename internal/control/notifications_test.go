package control

import (
	"bytes"
	"strings"
	"testing"

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
		User:   store.User{ID: uid, Username: username},
		Scope:  "full",
		Store:  st,
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
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
// truncation cut length is not returned unchanged. shortToken's short
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
