package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// The about text parked by migration 0058 becomes a file in the owner's
// .gitbay repository. Running it twice writes nothing the second time.
func TestMigrateProfileAbout(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// Seed the holding table the way the migration would have.
	dbPath := filepath.Join(inst.root, "gitbay.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(
		"INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format) "+
			"VALUES ('user', (SELECT id FROM users WHERE username='alice'), ?, 'org')",
		"* alice\n\ntext from the database\n")
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	inst.admin(t, "admin", "migrate-profile-about")

	out, _, code := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show: %d", code)
	}
	if !strings.Contains(out, "text from the database") {
		t.Errorf("about not moved into the repository: %s", out)
	}
	if !strings.Contains(out, `"about_path":"profile/README.org"`) {
		t.Errorf("about not written at the recorded format: %s", out)
	}

	// The repository it made is public, so nothing that was world-readable
	// became hidden.
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/.gitbay", "--json")
	if !strings.Contains(out, `"visibility":"public"`) {
		t.Errorf("backfilled repository is not public: %s", out)
	}

	// Idempotent: a second run is a no-op and the table stays empty.
	inst.admin(t, "admin", "migrate-profile-about")
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = st.DB.QueryRow("SELECT count(*) FROM profile_about_backfill").Scan(&n)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("holding table still has %d row(s)", n)
	}
}
