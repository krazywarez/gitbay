package store

import (
	"strconv"
	"strings"
	"testing"
)

// idMigration is the version of the migration that makes ids
// AUTOINCREMENT (#306), found by name so the test survives renumbering.
func idMigration(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.name == "id_autoincrement" {
			return m.version
		}
	}
	t.Fatal("no id_autoincrement migration")
	return 0
}

var autoincTables = []string{"users", "orgs", "repos", "api_tokens", "ssh_keys", "webhook_deliveries", "push_queue"}

func mustExec(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func fkClean(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid, fk any
		rows.Scan(&table, &rowid, &parent, &fk)
		t.Fatalf("foreign_key_check: %s row %v -> %s", table, rowid, parent)
	}
}

// seedForIDs fills every rebuilt table and the rows that name their ids,
// then deletes the newest repository and the newest account while a
// deploy key and a grant still name them.
func seedForIDs(t *testing.T, s *Store) (goneRepo, goneUser int64) {
	t.Helper()
	alice := mustExec(t, s, "INSERT INTO users (username) VALUES ('alice')")
	org := mustExec(t, s, "INSERT INTO orgs (name) VALUES ('acme')")
	repo := mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, 'app', 'public')", alice)
	mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility, fork_of) VALUES ('org', ?, 'fork', 'private', ?)", org, repo)
	tok := mustExec(t, s, "INSERT INTO api_tokens (user_id, name, token_hash) VALUES (?, 't1', 'h1')", alice)
	mustExec(t, s, "INSERT INTO api_tokens (user_id, name, token_hash, created_by_token) VALUES (?, 't2', 'h2', ?)", alice, tok)
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, created_by_token) VALUES (?, 'SHA256:a', 'ssh-ed25519', x'00', ?)", alice, tok)
	hook := mustExec(t, s, "INSERT INTO webhooks (repo_id, url) VALUES (?, 'https://example.org/h')", repo)
	ev := mustExec(t, s, "INSERT INTO events (repo_id, actor_id, kind) VALUES (?, ?, 'push')", repo, alice)
	mustExec(t, s, "INSERT INTO webhook_deliveries (webhook_id, event_id) VALUES (?, ?)", hook, ev)
	dev := mustExec(t, s, "INSERT INTO push_devices (user_id, token) VALUES (?, 'devtok')", alice)
	mustExec(t, s, "INSERT INTO push_queue (device_id, title, body, path) VALUES (?, 't', 'b', 'p')", dev)
	mustExec(t, s, "INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'admin')", org, alice)

	goneRepo = mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, 'gone', 'private')", alice)
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, 'SHA256:d', 'ssh-ed25519', x'00', ?)",
		alice, "deploy:"+itoa(goneRepo)+":rw")
	// Raw deletes: DeleteRepo and DeleteUser now take the deploy key and
	// the grant with them, and these orphans stand for ones left earlier.
	mustExec(t, s, "DELETE FROM repos WHERE id = ?", goneRepo)
	goneUser = mustExec(t, s, "INSERT INTO users (username) VALUES ('carol')")
	if err := s.GrantAccess(repo, goneUser, "write"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "DELETE FROM users WHERE id = ?", goneUser)
	return goneRepo, goneUser
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestIDMigrationKeepsRowsAndForeignKeys(t *testing.T) {
	v := idMigration(t)
	s := open(t)
	if err := s.MigrateTo(v - 1); err != nil {
		t.Fatal(err)
	}
	goneRepo, goneUser := seedForIDs(t, s)
	before := map[string]int{}
	for _, tbl := range append(autoincTables, "org_members", "webhooks", "events", "push_devices", "repo_access") {
		before[tbl] = count(t, s, tbl)
	}
	if err := s.MigrateTo(v); err != nil {
		t.Fatal(err)
	}
	for tbl, n := range before {
		if got := count(t, s, tbl); got != n {
			t.Errorf("%s: %d rows after the migration, want %d", tbl, got, n)
		}
	}
	for _, tbl := range autoincTables {
		var sql string
		if err := s.DB.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", tbl).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, "AUTOINCREMENT") {
			t.Errorf("%s is not AUTOINCREMENT", tbl)
		}
	}
	fkClean(t, s)

	// Children still name the rebuilt parents, not the *_old tables.
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" LIKE '%_old'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d foreign keys name a *_old table", n)
	}
	for _, c := range []struct{ child, parent string }{
		{"ssh_keys", "users"}, {"ssh_keys", "api_tokens"}, {"api_tokens", "api_tokens"},
		{"repos", "repos"}, {"issues", "repos"}, {"teams", "orgs"}, {"mr_merge_queue", "ssh_keys"},
		{"push_queue", "push_devices"}, {"webhook_deliveries", "webhooks"},
	} {
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE "table" = ?`, c.child, c.parent).Scan(&n); err != nil || n == 0 {
			t.Errorf("%s has no foreign key to %s (%v)", c.child, c.parent, err)
		}
	}
	for _, idx := range []string{"ssh_keys_user", "webhook_deliveries_due", "push_queue_due"} {
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}

	// The triggers still fire.
	alice, err := s.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM users WHERE id = ?", alice.ID); err == nil || !strings.Contains(err.Error(), "still owns repositories") {
		t.Fatalf("deleting a repository owner: %v", err)
	}
	if _, err := s.DB.Exec("DELETE FROM orgs WHERE name = 'acme'"); err == nil || !strings.Contains(err.Error(), "still owns repositories") {
		t.Fatalf("deleting an owning org: %v", err)
	}
	// Cascades still reach the rebuilt tables' children.
	mustExec(t, s, "DELETE FROM push_devices WHERE token = 'devtok'")
	if got := count(t, s, "push_queue"); got != 0 {
		t.Fatalf("push_queue after its device went: %d rows", got)
	}

	// The sequences start above the ids a deploy key and a grant still
	// name, though neither row survived.
	var seq int64
	s.DB.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = 'repos'").Scan(&seq)
	if seq < goneRepo {
		t.Fatalf("repos sequence %d, want at least %d", seq, goneRepo)
	}
	r, err := s.CreateRepo("user", alice.ID, "new", "public")
	if err != nil {
		t.Fatal(err)
	}
	if r <= goneRepo {
		t.Fatalf("new repository took id %d; a deploy key still names %d", r, goneRepo)
	}
	u, err := s.CreateUser("dave", false)
	if err != nil {
		t.Fatal(err)
	}
	if u <= goneUser {
		t.Fatalf("new account took id %d; a grant still names %d", u, goneUser)
	}

	// Down and up again keep the rows.
	if err := s.MigrateTo(v - 1); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "users"); got != before["users"]+1 {
		t.Fatalf("users after down: %d", got)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'users'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("sqlite_sequence keeps users after down: %d, %v", n, err)
	}
	fkClean(t, s)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	fkClean(t, s)
}

// A parked profile about text names its owner by kind and id with no
// foreign key; the sequences start above the ids it names (#306).
func TestIDMigrationSeedsFromAboutBackfill(t *testing.T) {
	v := idMigration(t)
	s := open(t)
	if err := s.MigrateTo(v - 1); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "INSERT INTO users (username) VALUES ('alice')")
	mustExec(t, s, "INSERT INTO orgs (name) VALUES ('acme')")
	mustExec(t, s, `INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
		VALUES ('user', 50, 'a', 'md'), ('org', 40, 'b', 'md')`)
	if err := s.MigrateTo(v); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int64{"users": 50, "orgs": 40} {
		var seq int64
		if err := s.DB.QueryRow("SELECT seq FROM sqlite_sequence WHERE name = ?", table).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if seq < want {
			t.Errorf("%s sequence %d, want at least %d", table, seq, want)
		}
	}
}

// Deleting the row with the highest id does not free that id, in any of
// the rebuilt tables.
func TestIDsNotReusedAfterDelete(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	u1 := mustExec(t, s, "INSERT INTO users (username) VALUES ('u1')")
	u2 := mustExec(t, s, "INSERT INTO users (username) VALUES ('u2')")
	repo := mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, 'r', 'public')", u1)
	hook := mustExec(t, s, "INSERT INTO webhooks (repo_id, url) VALUES (?, 'https://example.org/h')", repo)
	ev := mustExec(t, s, "INSERT INTO events (repo_id, kind) VALUES (?, 'push')", repo)
	dev := mustExec(t, s, "INSERT INTO push_devices (user_id, token) VALUES (?, 'devtok')", u1)

	cases := []struct {
		table, insert string
		args          func(i int) []any
	}{
		{"users", "INSERT INTO users (username) VALUES (?)", func(i int) []any { return []any{"x" + itoa(int64(i))} }},
		{"orgs", "INSERT INTO orgs (name) VALUES (?)", func(i int) []any { return []any{"o" + itoa(int64(i))} }},
		{"repos", "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, ?, 'public')",
			func(i int) []any { return []any{u1, "n" + itoa(int64(i))} }},
		{"api_tokens", "INSERT INTO api_tokens (user_id, name, token_hash) VALUES (?, ?, ?)",
			func(i int) []any { return []any{u2, "t" + itoa(int64(i)), "h" + itoa(int64(i))} }},
		{"ssh_keys", "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (?, ?, 'ssh-ed25519', x'00')",
			func(i int) []any { return []any{u2, "SHA256:" + itoa(int64(i))} }},
		{"webhook_deliveries", "INSERT INTO webhook_deliveries (webhook_id, event_id) VALUES (?, ?)",
			func(int) []any { return []any{hook, ev} }},
		{"push_queue", "INSERT INTO push_queue (device_id, title, body, path) VALUES (?, 't', 'b', 'p')",
			func(int) []any { return []any{dev} }},
	}
	for _, c := range cases {
		first := mustExec(t, s, c.insert, c.args(1)...)
		mustExec(t, s, "DELETE FROM "+c.table+" WHERE id = ?", first)
		second := mustExec(t, s, c.insert, c.args(2)...)
		if second <= first {
			t.Errorf("%s: id %d handed out again after its row was deleted (got %d)", c.table, first, second)
		}
	}
}

// A sender marks a webhook delivery or a push by id after its request
// returns. If the hook or device was removed meanwhile, the cascade took
// the row, and the next row must not take its id and its mark (#306).
func TestInFlightMarksAfterCascade(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	u := mustExec(t, s, "INSERT INTO users (username) VALUES ('u')")
	repo := mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, 'r', 'public')", u)
	ev := mustExec(t, s, "INSERT INTO events (repo_id, kind) VALUES (?, 'push')", repo)
	h1 := mustExec(t, s, "INSERT INTO webhooks (repo_id, url) VALUES (?, 'https://example.org/1')", repo)
	h2 := mustExec(t, s, "INSERT INTO webhooks (repo_id, url) VALUES (?, 'https://example.org/2')", repo)
	inFlight := mustExec(t, s, "INSERT INTO webhook_deliveries (webhook_id, event_id) VALUES (?, ?)", h1, ev)
	if err := s.RemoveWebhook(repo, h1); err != nil {
		t.Fatal(err)
	}
	next := mustExec(t, s, "INSERT INTO webhook_deliveries (webhook_id, event_id) VALUES (?, ?)", h2, ev)
	if err := s.MarkDelivered(inFlight, 200); err != nil {
		t.Fatal(err)
	}
	var delivered *string
	if err := s.DB.QueryRow("SELECT delivered_at FROM webhook_deliveries WHERE id = ?", next).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != nil {
		t.Fatal("the removed hook's delivery was marked on the next hook's")
	}

	d1 := mustExec(t, s, "INSERT INTO push_devices (user_id, token) VALUES (?, 'd1')", u)
	d2 := mustExec(t, s, "INSERT INTO push_devices (user_id, token) VALUES (?, 'd2')", u)
	pushing := mustExec(t, s, "INSERT INTO push_queue (device_id, title, body, path) VALUES (?, 't', 'b', 'p')", d1)
	if err := s.RemovePushDevice(u, d1); err != nil {
		t.Fatal(err)
	}
	queued := mustExec(t, s, "INSERT INTO push_queue (device_id, title, body, path) VALUES (?, 't', 'b', 'p')", d2)
	if err := s.MarkPushSent(pushing); err != nil {
		t.Fatal(err)
	}
	var sent *string
	if err := s.DB.QueryRow("SELECT sent_at FROM push_queue WHERE id = ?", queued).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if sent != nil {
		t.Fatal("the removed device's push was marked on the next device's")
	}
}

// Deleting a repository takes the deploy keys scoped to it, and only
// those; deleting an account or an org takes its grants (#306).
func TestDeletesTakeGrantsAndDeployKeys(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var revoked []Revoked
	s.OnRevoke(func(r Revoked) { revoked = append(revoked, r) })
	alice := mustExec(t, s, "INSERT INTO users (username) VALUES ('alice')")
	var repos []int64
	for i := range 12 {
		repos = append(repos, mustExec(t, s,
			"INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, ?, 'public')", alice, "r"+itoa(int64(i))))
	}
	// repos[0] is 2 and repos[10] is 12: a prefix of one id must not
	// match the other.
	gone, other := repos[0], repos[len(repos)-2]
	if !strings.HasPrefix(itoa(other), itoa(gone)) {
		t.Fatalf("ids %d and %d do not share a prefix", gone, other)
	}
	goneKey := mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, 'SHA256:g', 'a', x'00', ?)",
		alice, "deploy:"+itoa(gone)+":rw")
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, 'SHA256:o', 'a', x'00', ?)",
		alice, "deploy:"+itoa(other)+":ro")
	if err := s.DeleteRepo(gone); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = 'SHA256:g'").Scan(&n)
	if n != 0 {
		t.Fatal("the deleted repository's deploy key survived")
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = 'SHA256:o'").Scan(&n)
	if n != 1 {
		t.Fatal("another repository's deploy key went with it")
	}
	if len(revoked) != 1 || len(revoked[0].KeyIDs) != 1 || revoked[0].KeyIDs[0] != goneKey {
		t.Fatalf("revocations announced: %+v", revoked)
	}
	if err := s.DeleteRepo(gone); err != ErrNotFound {
		t.Fatalf("deleting it again: %v", err)
	}

	bob := mustExec(t, s, "INSERT INTO users (username) VALUES ('bob')")
	org := mustExec(t, s, "INSERT INTO orgs (name) VALUES ('acme')")
	if err := s.GrantAccess(other, bob, "write"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'org', ?, 'read')", other, org)
	// An org with the same id as bob's keeps its grant when bob goes.
	mustExec(t, s, "INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'org', ?, 'read')", repos[1], bob)
	mustExec(t, s, `INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
		VALUES ('user', ?, 'a', 'md'), ('org', ?, 'b', 'md'), ('org', ?, 'c', 'md')`, bob, org, bob)
	if err := s.DeleteUser(bob); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_access WHERE subject_kind = 'user' AND subject_id = ?", bob).Scan(&n)
	if n != 0 {
		t.Fatal("the deleted account's grant survived")
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM profile_about_backfill WHERE owner_kind = 'user' AND owner_id = ?", bob).Scan(&n)
	if n != 0 {
		t.Fatal("the deleted account's about text survived")
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_access WHERE subject_kind = 'org' AND subject_id = ?", bob).Scan(&n)
	if n != 1 {
		t.Fatal("an org grant went with the account of the same id")
	}
	if err := s.DeleteOrg(org); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_access WHERE subject_kind = 'org' AND subject_id = ?", org).Scan(&n)
	if n != 0 {
		t.Fatal("the deleted org's grant survived")
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM profile_about_backfill WHERE owner_kind = 'org'").Scan(&n)
	if n != 1 {
		t.Fatalf("org about texts after deleting acme: %d, want only the one with bob's id", n)
	}
}

// The cleanup migration removes grants and deploy keys left by earlier
// deletes, keeps live ones, and leaves a note with the counts.
func TestOrphanCleanupMigration(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v := 0
	for _, m := range ms {
		if m.name == "orphan_grants_deploy_keys" {
			v = m.version
		}
	}
	if v == 0 {
		t.Fatal("no orphan_grants_deploy_keys migration")
	}
	s := open(t)
	if err := s.MigrateTo(v - 1); err != nil {
		t.Fatal(err)
	}
	alice := mustExec(t, s, "INSERT INTO users (username) VALUES ('alice')")
	repo := mustExec(t, s, "INSERT INTO repos (owner_kind, owner_id, name, visibility) VALUES ('user', ?, 'r', 'public')", alice)
	mustExec(t, s, "INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'user', ?, 'read')", repo, alice)
	mustExec(t, s, "INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'user', 999, 'write')", repo)
	mustExec(t, s, "INSERT INTO repo_access (repo_id, subject_kind, subject_id, role) VALUES (?, 'org', 998, 'read')", repo)
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, 'SHA256:live', 'a', x'00', ?)",
		alice, "deploy:"+itoa(repo)+":rw")
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, 'SHA256:dead', 'a', x'00', 'deploy:997:ro')", alice)
	mustExec(t, s, "INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (?, 'SHA256:user', 'a', x'00')", alice)
	mustExec(t, s, `INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
		VALUES ('user', ?, 'live', 'md'), ('user', 996, 'dead', 'md'), ('org', 995, 'dead', 'md')`, alice)
	var epoch int
	s.DB.QueryRow("SELECT value FROM settings WHERE key = 'key_epoch'").Scan(&epoch)
	if err := s.MigrateTo(v); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "repo_access"); got != 1 {
		t.Fatalf("repo_access: %d rows, want the live grant", got)
	}
	var about string
	if err := s.DB.QueryRow("SELECT group_concat(about) FROM profile_about_backfill").Scan(&about); err != nil || about != "live" {
		t.Fatalf("about texts after cleanup: %q, %v", about, err)
	}
	var fps []string
	rows, err := s.DB.Query("SELECT fingerprint FROM ssh_keys ORDER BY fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var fp string
		rows.Scan(&fp)
		fps = append(fps, fp)
	}
	rows.Close()
	if strings.Join(fps, " ") != "SHA256:live SHA256:user" {
		t.Fatalf("keys after cleanup: %v", fps)
	}
	var after int
	s.DB.QueryRow("SELECT value FROM settings WHERE key = 'key_epoch'").Scan(&after)
	if after != epoch+1 {
		t.Fatalf("key_epoch %d, want %d", after, epoch+1)
	}
	note, err := s.TakeMigrationNote()
	if err != nil || note != "removed grants of deleted accounts or organizations: 2; deploy keys of deleted repositories: 1; profile about texts of deleted accounts or organizations: 2" {
		t.Fatalf("note %q, %v", note, err)
	}
	if note, _ := s.TakeMigrationNote(); note != "" {
		t.Fatalf("note not cleared: %q", note)
	}

	// Nothing to remove, no note.
	if err := s.MigrateTo(v - 1); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(v); err != nil {
		t.Fatal(err)
	}
	if note, _ := s.TakeMigrationNote(); note != "" {
		t.Fatalf("note with nothing removed: %q", note)
	}
}
