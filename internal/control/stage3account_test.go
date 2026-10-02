package control

import (
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// accountFixture is a user with something in every account list task 4
// migrates, an org they admin, and a profile.
func accountFixture(t *testing.T) (*store.Store, store.User) {
	t.Helper()
	st, _, uid := newQueueTestRepo(t)
	u := store.User{ID: uid, Username: "alice", IsAdmin: true}
	if err := st.AddSSHKey(uid, "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ssh-ed25519", []byte("blob"), "full", "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEmail(uid, "alice@example.test", "smtp", true); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEmail(uid, "old@example.test", "", false); err != nil {
		t.Fatal(err)
	}
	if err := st.AddPGPKey(uid, "ABCDEF0123456789ABCDEF0123456789ABCDEF01", "armored", `["Alice <alice@example.test>"]`, nil, nil); err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := st.CreateAPIToken(uid, "ci", strings.Repeat("ab", 32), "read", &expires, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession(strings.Repeat("cd", 32), uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	dispatchAs(t, st, u, "", "org", "create", "acme")
	dispatchAs(t, st, u, "", "profile", "set", "--description", "Builds forges", "--website", "https://alice.example.test")
	return st, u
}

func TestAccountPlainPinned(t *testing.T) {
	st, u := accountFixture(t)
	for name, argv := range map[string][]string{
		"keys-list":         {"keys", "list"},
		"email-list":        {"email", "list"},
		"pgp-list":          {"pgp", "list"},
		"token-list":        {"token", "list"},
		"web-sessions-list": {"web", "sessions", "list"},
		"whoami":            {"whoami"},
		"profile-show":      {"profile", "show"},
		"org-profile":       {"org", "profile", "acme"},
	} {
		pinPlain(t, name, dispatchAs(t, st, u, "", argv...))
	}
	pinPlain(t, "profile-set", dispatchAs(t, st, u, "", "profile", "set", "--description", "Builds forges"))
}

func TestAccountScreens(t *testing.T) {
	st, u := accountFixture(t)
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"keys", "list"}, []string{"SSH keys (1)\n", "full  laptop"}},
		{[]string{"email", "list"}, []string{"Emails (2)\n", "alice@example.test     verified    primary · smtp", "old@example.test    ●  unverified"}},
		{[]string{"pgp", "list"}, []string{"OpenPGP keys (1)\n", "Alice <alice@example.test>"}},
		{[]string{"token", "list"}, []string{"API tokens (1)\n", "ci  read"}},
		{[]string{"web", "sessions", "list"}, []string{"Browser sessions (1)\n", "cdcdcdcdcdcd"}},
		{[]string{"whoami"}, []string{"User:", "alice", "Role:", "admin"}},
		{[]string{"profile", "show"}, []string{"Profile:", "alice  user · Builds forges", "Website:", "https://alice.example.test", "Repos (1)\n"}},
		{[]string{"profile", "set", "--description", "Builds forges"}, []string{"Profile:", "alice"}},
		{[]string{"org", "profile", "acme"}, []string{"Profile:", "acme  org"}},
	} {
		out := atTerminal(t, st, u, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		if strings.Contains(out, strings.Repeat("ab", 32)) {
			t.Errorf("%v: token hash on screen", tc.argv)
		}
		checkLegend(t, out)
	}
}
