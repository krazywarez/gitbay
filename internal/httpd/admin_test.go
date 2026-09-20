package httpd

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// The admin page renders every queue dashboard reports, push included,
// and names a push by its device id: a token is device-identifying and
// reaches an admin's page no more than it reaches its owner's.
func TestAdminPageShowsThePushQueue(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushEnabled(uid, true); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 64)
	device, err := st.AddPushDevice(uid, token, "iphone")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueuePush(uid, "krz/gitbay", "alice opened #1", "/krz/gitbay/issues/1"); err != nil {
		t.Fatal(err)
	}
	// Only rows that are retrying or dead-lettered are listed, so fail
	// the queued one first.
	due, err := st.DuePush(10)
	if err != nil || len(due) != 1 {
		t.Fatalf("DuePush: %v %v", due, err)
	}
	if err := st.MarkPushFailed(due[0].ID, "403 InvalidProviderToken", nil); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st)
	rr := httptest.NewRecorder()
	s.adminPage(rr, httptest.NewRequest("GET", "/admin", nil), store.User{ID: uid, Username: "root", IsAdmin: true})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="push"`) {
		t.Fatalf("no push section:\n%s", body)
	}
	if !strings.Contains(body, `href="#push"`) {
		t.Fatalf("push is missing from the jump list:\n%s", body)
	}
	if !strings.Contains(body, "device "+strconv.FormatInt(device, 10)) {
		t.Fatalf("the dead-lettered push is not listed by device id:\n%s", body)
	}
	if !strings.Contains(body, "403 InvalidProviderToken") {
		t.Fatalf("the dead-lettered push's error is not shown:\n%s", body)
	}
	if strings.Contains(body, token) {
		t.Fatalf("the page printed a device token:\n%s", body)
	}
}
