package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// notifyFixture is a user with an unread and a read notification, a push
// device, and a repository with a webhook and one delivery.
func notifyFixture(t *testing.T) (*store.Store, store.Repo, store.User) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	u := store.User{ID: uid, Username: "alice"}
	if err := st.AddNotice(uid, repo.ID, "issue", "bob", "opened #3 Crash on start", repo.Path()+"/issues/3"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNotice(uid, repo.ID, "mr", "bob", "opened !4 Fix crash", repo.Path()+"/mrs/4"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPushDevice(uid, strings.Repeat("f", 64), "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddWebhook(repo.ID, "https://hooks.example.test/in/s3cretpath", "whsecret", "issue.open"); err != nil {
		t.Fatal(err)
	}
	st.RecordEvent(repo.ID, uid, "issue.open", `{"number":3}`)
	return st, repo, u
}

func TestNotifyPlainPinned(t *testing.T) {
	st, repo, u := notifyFixture(t)
	p := repo.Path()
	for name, argv := range map[string][]string{
		"notifications-list":          {"notifications", "list"},
		"notifications-settings-show": {"notifications", "settings", "show"},
		"notifications-device-list":   {"notifications", "device", "list"},
		"webhook-list":                {"webhook", "list", p},
		"webhook-deliveries":          {"webhook", "deliveries", p},
	} {
		pinPlain(t, name, dispatchAs(t, st, u, "", argv...))
	}
	pinPlain(t, "notifications-settings-mail", dispatchAs(t, st, u, "", "notifications", "settings", "mail", "off"))
}

func TestNotifyScreens(t *testing.T) {
	st, repo, u := notifyFixture(t)
	p := repo.Path()
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"notifications", "list"}, []string{"Notifications (2)\n", "●", "bob opened !4 Fix crash", "gitbay notifications read --all"}},
		{[]string{"notifications", "settings", "show"}, []string{"Mail:", "Reply:", "Watch:", "Push:", "gitbay notifications settings mail "}},
		{[]string{"notifications", "settings", "mail", "off"}, []string{"Mail:   off", "gitbay notifications settings mail on"}},
		{[]string{"notifications", "device", "list"}, []string{"Push devices (1)\n", "phone"}},
		{[]string{"webhook", "list", p}, []string{"Webhooks (1)\n", "https://hooks.example.test/…", "issue.open · signed"}},
		{[]string{"webhook", "deliveries", p}, []string{"Deliveries (1)\n", "◐", "issue.open"}},
	} {
		out := atTerminal(t, st, u, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		for _, secret := range []string{"whsecret", "s3cretpath", strings.Repeat("f", 64)} {
			if strings.Contains(out, secret) {
				t.Errorf("%v: %q on screen:\n%s", tc.argv, secret, out)
			}
		}
		checkLegend(t, out)
	}
}
