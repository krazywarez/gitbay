package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateUpDown(t *testing.T) {
	s := open(t)

	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	v, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if v < 1 {
		t.Fatalf("version %d after MigrateUp", v)
	}

	// Seeded settings row exists.
	var epoch string
	if err := s.DB.QueryRow("SELECT value FROM settings WHERE key = 'key_epoch'").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != "1" {
		t.Fatalf("key_epoch = %q, want 1", epoch)
	}

	// Down to empty, then back up.
	if err := s.MigrateTo(0); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d tables remain after down-migration to 0", n)
	}
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	// Idempotent at latest.
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyFingerprintGloballyUnique(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec("INSERT INTO users (username) VALUES ('alice'), ('bob')")
	mustExec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (1, 'SHA256:aaa', 'ed25519', x'00')")

	// Same fingerprint on a different account must be rejected.
	_, err := s.DB.Exec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (2, 'SHA256:aaa', 'ed25519', x'00')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate ssh fingerprint across accounts: err = %v, want UNIQUE violation", err)
	}

	mustExec("INSERT INTO pgp_keys (user_id, fingerprint, armored) VALUES (1, 'FPR1', '-----')")
	_, err = s.DB.Exec("INSERT INTO pgp_keys (user_id, fingerprint, armored) VALUES (2, 'FPR1', '-----')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate pgp fingerprint across accounts: err = %v, want UNIQUE violation", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (999, 'SHA256:zzz', 'ed25519', x'00')")
	if err == nil {
		t.Fatal("insert with dangling user_id succeeded; foreign keys are off")
	}
}

// The database file carries token hashes, addresses and private repo names.
// The directory above it is the real boundary; this is the second one.
func TestDatabaseFileIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitbay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o007 != 0 {
		t.Errorf("database mode %04o is other-readable", mode)
	}
}

func TestSSHKeyLabel(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKey(uid, "SHA256:aaa", "ssh-ed25519", []byte{0}, "full", "laptop"); err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListSSHKeys(uid)
	if err != nil || len(keys) != 1 || keys[0].Label != "laptop" {
		t.Fatalf("ListSSHKeys = %+v, %v; want one key labelled laptop", keys, err)
	}
	if err := s.SetSSHKeyLabel(uid, "SHA256:aaa", "desk"); err != nil {
		t.Fatal(err)
	}
	k, err := s.SSHKeyByFingerprint("SHA256:aaa")
	if err != nil || k.Label != "desk" {
		t.Fatalf("SSHKeyByFingerprint after relabel: %+v, %v", k, err)
	}
	// Only the owner may relabel; someone else's fingerprint is not found.
	if err := s.SetSSHKeyLabel(uid+1, "SHA256:aaa", "x"); err != ErrNotFound {
		t.Fatalf("relabel by another user: %v, want ErrNotFound", err)
	}
}

// Migration 0052 rebuilds labels and milestones with an org scope. Foreign
// keys are off for the migration: rebuilding a parent table with children
// (issue_labels, issues.milestone_id) otherwise loses the children's rows.
// legacy_alter_table keeps the children naming labels and milestones
// through the rename, so they bind to the new tables rather than to
// labels_old/milestones_old. foreign_key_check afterwards proves the ids
// line up. This checks the ids, the memberships and the foreign keys all
// survive.
func TestMigration0052KeepsMembershipsAndForeignKeys(t *testing.T) {
	s := open(t)
	if err := s.MigrateTo(51); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	number, err := s.CreateIssue(rid, uid, "one", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	// CreateIssue returns the per-repo number; the rows below reference
	// the issues.id row.
	issue, err := s.IssueByNumber(rid, number)
	if err != nil {
		t.Fatal(err)
	}
	iid := issue.ID
	if _, err := s.DB.Exec("INSERT INTO labels (repo_id, name, color) VALUES (?, 'bug', '#ff0000')", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) SELECT ?, id FROM labels WHERE name = 'bug'", iid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO milestones (repo_id, title) VALUES (?, 'v1')", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE issues SET milestone_id = (SELECT id FROM milestones WHERE title = 'v1') WHERE id = ?", iid); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(52); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issue_labels il JOIN labels l ON l.id = il.label_id
		WHERE il.issue_id = ? AND l.name = 'bug' AND l.repo_id = ? AND l.org_id IS NULL`, iid, rid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("label membership after 0052: %d, %v", n, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issues i JOIN milestones m ON m.id = i.milestone_id
		WHERE i.id = ? AND m.title = 'v1' AND m.repo_id = ?`, iid, rid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("milestone attachment after 0052: %d, %v", n, err)
	}
	rows, err := s.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after 0052")
	}
	// The scope CHECK holds: a row with neither or both scopes is refused.
	if _, err := s.DB.Exec("INSERT INTO labels (name) VALUES ('neither')"); err == nil {
		t.Fatal("label with no scope was accepted")
	}
	if _, err := s.DB.Exec("INSERT INTO labels (repo_id, org_id, name) VALUES (?, 1, 'both')", rid); err == nil {
		t.Fatal("label with both scopes was accepted")
	}
	// Down refuses while an org-scoped row exists, and works once it is gone.
	if _, err := s.DB.Exec("INSERT INTO orgs (name) VALUES ('acme')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO labels (org_id, name) VALUES ((SELECT id FROM orgs WHERE name = 'acme'), 'org-only')"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(51); err == nil {
		t.Fatal("down migration accepted an org-scoped label")
	}
	if _, err := s.DB.Exec("DELETE FROM labels WHERE org_id IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(51); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issue_labels il JOIN labels l ON l.id = il.label_id WHERE il.issue_id = ?`, iid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("label membership after down: %d, %v", n, err)
	}
}

// Migrating all the way up runs 0052's "-- foreign_keys: off" step on its
// own pinned connection and every other migration's script, which has no
// such directive, on the pool as usual. A fresh query afterwards still
// sees foreign keys on: the pinned connection re-enabled them before
// returning to the pool, and no other connection was ever touched.
func TestMigrationForeignKeysDirective(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := s.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys after MigrateUp: %d, want 1", fk)
	}
}
