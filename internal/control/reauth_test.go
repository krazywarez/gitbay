package control

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestStaleSignInBoundary(t *testing.T) {
	at := time.Now()
	if staleSignIn(at, at.Add(ReauthWindow)) {
		t.Error("exactly ReauthWindow counted as stale")
	}
	if !staleSignIn(at, at.Add(ReauthWindow+time.Second)) {
		t.Error("ReauthWindow plus a second counted as fresh")
	}
	if !staleSignIn(time.Time{}, at) {
		t.Error("a zero sign-in time counted as fresh")
	}
}

// A browser session runs NeedsRecentSignIn commands only within
// ReauthWindow of signing in; SSH, the API and the host carry no
// session and are not affected (#297).
func TestRecentSignInGate(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateUser("bob", false); err != nil {
		t.Fatal(err)
	}
	run := func(source string, signedIn time.Time, stdin string, argv ...string) (string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice", SignedInAt: signedIn})
		c.Cfg.Limits.WriteRate = -1
		c.Source = source
		c.ViaAPI = source == SourceWeb || source == "api"
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return strings.TrimSpace(errOut.String()), code
	}
	fresh := time.Now().Add(-time.Minute)
	stale := time.Now().Add(-ReauthWindow - time.Minute)
	staleKey := authorizedKey(t, "stale")

	for _, tc := range []struct {
		name     string
		signedIn time.Time
		stdin    string
		argv     []string
	}{
		{"stale keys add", stale, staleKey, []string{"keys", "add"}},
		{"stale token create", stale, "", []string{"token", "create", "--name", "x"}},
		{"stale repo access grant", stale, "", []string{"repo", "access", "grant", repo.Path(), "bob", "write"}},
		{"zero sign-in time", time.Time{}, authorizedKey(t, "zero"), []string{"keys", "add"}},
	} {
		if msg, code := run(SourceWeb, tc.signedIn, tc.stdin, tc.argv...); code != protocol.ExitDenied || msg != ReauthRefusal {
			t.Errorf("%s: exit %d, %q", tc.name, code, msg)
		}
	}
	if msg, code := run(SourceWeb, fresh, authorizedKey(t, "fresh"), "keys", "add"); code != protocol.ExitOK {
		t.Fatalf("fresh session: exit %d, %q", code, msg)
	}
	// SSH, the API and the host have no session; a zero SignedInAt is
	// what they carry.
	for _, source := range []string{"SHA256:abc", "api", "host"} {
		if msg, code := run(source, time.Time{}, authorizedKey(t, source), "keys", "add"); code != protocol.ExitOK {
			t.Fatalf("%s: exit %d, %q", source, code, msg)
		}
	}
	// A command that grants nothing is not held back.
	if msg, code := run(SourceWeb, stale, "", "keys", "list"); code != protocol.ExitOK {
		t.Fatalf("keys list on a stale session: exit %d, %q", code, msg)
	}
	keys, err := st.ListSSHKeys(uid)
	if err != nil || len(keys) != 4 {
		t.Fatalf("keys: %d %v, want the fresh, ssh, api and host ones", len(keys), err)
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("refusal audit rows: %+v", got)
	}
	keyText := strings.Fields(staleKey)[1]
	for _, e := range got {
		if strings.Contains(e.Data, keyText) {
			t.Errorf("%s kept the key: %s", e.Action, e.Data)
		}
	}
}

// The set of commands a stale web session is refused. Adding one is a
// decision; it shows up here.
func TestNeedsRecentSignInSet(t *testing.T) {
	var got []string
	for _, cmd := range Commands() {
		if cmd.MintsCredential && !cmd.NeedsRecentSignIn {
			t.Errorf("%s mints a credential without NeedsRecentSignIn", joinPath(cmd.Path))
		}
		if cmd.NeedsRecentSignIn {
			got = append(got, joinPath(cmd.Path))
		}
	}
	slices.Sort(got)
	want := []string{
		"admin email verify",
		"admin invite",
		"admin user create",
		"admin user enable",
		"admin user promote",
		"email verify",
		"keys add",
		"notifications device add",
		"org members add",
		"org settings members-role",
		"org team add",
		"org team grant",
		"pgp add",
		"repo access grant",
		"repo deploy-key add",
		"repo mirror add",
		"repo runner add",
		"repo secret set",
		"repo transfer",
		"token create",
		"web login",
		"webhook add",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("NeedsRecentSignIn commands:\n got %q\nwant %q", got, want)
	}
}
