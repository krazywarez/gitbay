package control

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// runAs dispatches argv as u and returns the exit code and stderr.
func runAs(st *store.Store, u store.User, root string, argv ...string) (int, string) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: out, Stderr: errOut, Stdin: strings.NewReader("")}
	c.Cfg.Server.Root = root
	c.Cfg.Server.SiteURL = "https://forge.test/"
	c.Cfg.Limits.WriteRate = -1
	return Dispatch(c, argv), errOut.String()
}

func deleteFixture(t *testing.T) (*store.Store, string, store.User, store.User) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	user := func(name string) store.User {
		id, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := st.UserByID(id)
		return u
	}
	return st, root, user("alice"), user("bob")
}

// account delete refuses a mistyped name, an account with no verified
// address, and the only admin of an org; otherwise it mails a link and
// changes nothing else.
func TestAccountDeleteRequest(t *testing.T) {
	st, root, _, bob := deleteFixture(t)
	if code, errOut := runAs(st, bob, root, "account", "delete", "--confirm", "bob"); code != protocol.ExitDenied || !strings.Contains(errOut, "verify an address") {
		t.Fatalf("no address: exit %d %q", code, errOut)
	}
	if err := st.AddEmail(bob.ID, "bob@example.test", "smtp", true); err != nil {
		t.Fatal(err)
	}
	if code, _ := runAs(st, bob, root, "account", "delete", "--confirm", "bobb"); code != protocol.ExitUsage {
		t.Fatalf("mistyped name: exit %d", code)
	}
	// The only instance admin is refused.
	soleAdmin := bob
	soleAdmin.IsAdmin = true
	st.DB.Exec("UPDATE users SET is_admin = 1 WHERE id = ?", bob.ID)
	if code, errOut := runAs(st, soleAdmin, root, "account", "delete", "--confirm", "bob"); code != protocol.ExitDenied || !strings.Contains(errOut, "only admin") {
		t.Fatalf("sole instance admin: exit %d %q", code, errOut)
	}
	st.DB.Exec("UPDATE users SET is_admin = 0 WHERE id = ?", bob.ID)
	if code, _ := runAs(st, bob, root, "org", "create", "acme"); code != 0 {
		t.Fatal("org create")
	}
	if code, errOut := runAs(st, bob, root, "account", "delete", "--confirm", "bob"); code != protocol.ExitDenied || !strings.Contains(errOut, "only admin of acme") {
		t.Fatalf("sole org admin: exit %d %q", code, errOut)
	}
	if _, err := st.DB.Exec("DELETE FROM orgs WHERE name = 'acme'"); err != nil {
		t.Fatal(err)
	}
	if code, errOut := runAs(st, bob, root, "account", "delete", "--confirm", "bob"); code != 0 {
		t.Fatalf("request: exit %d %q", code, errOut)
	}
	var body string
	if err := st.DB.QueryRow("SELECT body FROM notifications WHERE recipient = 'bob@example.test'").Scan(&body); err != nil ||
		!strings.Contains(body, "https://forge.test/settings/delete?token=") {
		t.Fatalf("mail: %v %q", err, body)
	}
	if u, _ := st.UserByID(bob.ID); u.Disabled || u.DeleteAfter != "" {
		t.Fatal("a request alone changed the account")
	}
	if code, _ := runAs(st, bob, root, "account", "delete", "--cancel"); code != 0 {
		t.Fatal("cancel")
	}
	if code, _ := runAs(st, bob, root, "account", "delete", "--cancel"); code != protocol.ExitNotFound {
		t.Fatalf("second cancel: exit %d", code)
	}
}

// A confirmed deletion disables the account; the purge removes its
// repositories and the account, and moves what it wrote elsewhere to the
// ghost. Cancelling restores the account, and an admin decision clears
// the schedule.
func TestPurgeDueAccounts(t *testing.T) {
	st, root, alice, bob := deleteFixture(t)
	runAs(st, alice, root, "repo", "create", "alice/app")
	if code, errOut := runAs(st, bob, root, "repo", "create", "bob/own"); code != 0 {
		t.Fatalf("bob repo: %s", errOut)
	}
	app, _ := st.RepoByPath("alice/app")
	issue, err := st.CreateIssue(app.ID, bob.ID, "from bob", "", "md")
	if err != nil {
		t.Fatal(err)
	}

	schedule := func(u store.User, after time.Time) store.User {
		t.Helper()
		_, hash, _ := store.NewToken()
		if err := st.RequestAccountDeletion(u.ID, hash, time.Hour); err != nil {
			t.Fatal(err)
		}
		s, err := st.ConfirmAccountDeletion(hash, after)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Cancelling (what signing in does) restores the account.
	s := schedule(bob, time.Now().Add(time.Hour))
	if code, errOut := runAs(st, s, root, "whoami"); code != protocol.ExitDenied || !strings.Contains(errOut, "scheduled for deletion") {
		t.Fatalf("scheduled account: exit %d %q", code, errOut)
	}
	if !CancelScheduledDeletion(st, &s, "test") || s.Disabled {
		t.Fatal("cancel did not restore the account")
	}
	// A link opened after an admin suspended the account schedules
	// nothing.
	_, hash, _ := store.NewToken()
	st.RequestAccountDeletion(bob.ID, hash, time.Hour)
	st.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", bob.ID)
	if _, err := st.ConfirmAccountDeletion(hash, time.Now()); err == nil {
		t.Fatal("a suspended account was scheduled")
	}
	st.DB.Exec("UPDATE users SET disabled = 0 WHERE id = ?", bob.ID)
	// Once the purge has claimed an account, signing in cannot cancel.
	s = schedule(bob, time.Now().Add(-time.Minute))
	if ok, err := st.ClaimDeletion(bob.ID, time.Now()); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	s, _ = st.UserByID(bob.ID)
	if CancelScheduledDeletion(st, &s, "test") {
		t.Fatal("cancelled a purge in progress")
	}
	st.DB.Exec("UPDATE users SET disabled = 0, delete_after = NULL WHERE id = ?", bob.ID)
	// An admin disabling a scheduled account keeps it, disabled.
	schedule(bob, time.Now().Add(-time.Minute))
	if err := st.SetUserDisabled(bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if due, _ := st.DueDeletions(time.Now()); len(due) != 0 {
		t.Fatal("an admin's disable left the purge scheduled")
	}
	st.SetUserDisabled(bob.ID, false)

	schedule(bob, time.Now().Add(-time.Minute))
	cfg := config.Default()
	cfg.Server.Root = root
	purged, err := PurgeDueAccounts(cfg, st, time.Now())
	if err != nil || len(purged) != 1 || purged[0] != "bob" {
		t.Fatalf("purge: %v %v", purged, err)
	}
	if _, err := st.UserByID(bob.ID); err == nil {
		t.Fatal("bob still exists")
	}
	if _, err := os.Stat(RepoDir(root, "bob", "own")); !os.IsNotExist(err) {
		t.Fatalf("bob/own directory: %v", err)
	}
	ghost, err := st.UserByUsername("ghost")
	if err != nil || !ghost.Ghost || !ghost.Disabled {
		t.Fatalf("ghost: %+v %v", ghost, err)
	}
	var author int64
	st.DB.QueryRow("SELECT author_id FROM issues WHERE id = ?", issue).Scan(&author)
	if author != ghost.ID {
		t.Fatalf("issue author %d, want ghost %d", author, ghost.ID)
	}
	admin := alice
	admin.IsAdmin = true
	if code, _ := runAs(st, admin, root, "admin", "user", "enable", "ghost"); code != protocol.ExitDenied {
		t.Fatalf("admin enable ghost: exit %d", code)
	}

	// The only admin of an org is skipped and stays scheduled.
	carolID, _ := st.CreateUser("carol", false)
	carol, _ := st.UserByID(carolID)
	runAs(st, carol, root, "org", "create", "solo")
	schedule(carol, time.Now().Add(-time.Minute))
	if purged, err := PurgeDueAccounts(cfg, st, time.Now()); err != nil || len(purged) != 0 {
		t.Fatalf("sole admin purged: %v %v", purged, err)
	}
	if u, err := st.UserByID(carolID); err != nil || u.DeleteAfter == "" {
		t.Fatalf("carol: %+v %v", u, err)
	}
}
