package store

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/seal"
)

// keyedStore is a migrated store with a key file of one key.
func keyedStore(t *testing.T) (*Store, string, int64, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret.key")
	k, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := seal.WriteKeys(path, []seal.Key{k}); err != nil {
		t.Fatal(err)
	}
	ring, err := seal.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetKeyring(ring)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, path, uid, repoID
}

// raw reads every stored value of the secret columns.
func raw(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, sc := range secretColumns {
		rows, err := s.DB.Query("SELECT " + sc.column + " FROM " + sc.table + " WHERE " + sc.column + " != ''")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		rows.Close()
	}
	return out
}

func TestSecretColumnsAreSealed(t *testing.T) {
	s, _, uid, repoID := keyedStore(t)
	if err := s.SetBuildSecret(repoID, "DEPLOY", "ci-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWebhook(repoID, "https://hook.example/x", "hook-secret", "*"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMirror(repoID, "push", "https://mirror.example/r.git", "u", "mirror-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPushDevice(uid, "apns-token", "phone"); err != nil {
		t.Fatal(err)
	}

	vals := raw(t, s)
	if len(vals) != 4 {
		t.Fatalf("stored %d values, want 4: %v", len(vals), vals)
	}
	for _, v := range vals {
		if !seal.IsSealed(v) {
			t.Errorf("stored in clear: %q", v)
		}
		for _, plain := range []string{"ci-secret", "hook-secret", "mirror-token", "apns-token"} {
			if strings.Contains(v, plain) {
				t.Errorf("%q carries %q", v, plain)
			}
		}
	}

	secrets, err := s.BuildSecrets(repoID)
	if err != nil || secrets["DEPLOY"] != "ci-secret" {
		t.Fatalf("BuildSecrets = %v, %v", secrets, err)
	}
	hooks, err := s.ListWebhooks(repoID)
	if err != nil || len(hooks) != 1 || hooks[0].Secret != "hook-secret" {
		t.Fatalf("ListWebhooks = %+v, %v", hooks, err)
	}
	ms, err := s.ListMirrors(repoID)
	if err != nil || len(ms) != 1 || ms[0].Token != "mirror-token" {
		t.Fatalf("ListMirrors = %+v, %v", ms, err)
	}
	due, err := s.DueMirrors(3600)
	if err != nil || len(due) != 1 || due[0].Token != "mirror-token" {
		t.Fatalf("DueMirrors = %+v, %v", due, err)
	}
	ds, err := s.PushDevices(uid)
	if err != nil || len(ds) != 1 || ds[0].Token != "apns-token" {
		t.Fatalf("PushDevices = %+v, %v", ds, err)
	}

	// An empty webhook secret or mirror token stays empty: it means none.
	if _, err := s.AddWebhook(repoID, "https://hook.example/y", "", "*"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMirror(repoID, "push", "https://mirror.example/s.git", "", ""); err != nil {
		t.Fatal(err)
	}
	var empty int
	s.DB.QueryRow("SELECT (SELECT COUNT(*) FROM webhooks WHERE secret = '') + (SELECT COUNT(*) FROM mirrors WHERE token = '')").Scan(&empty)
	if empty != 2 {
		t.Errorf("empty values stored as %d rows of '', want 2", empty)
	}
}

// The queue readers open what they join.
func TestSealedQueueReaders(t *testing.T) {
	s, _, uid, repoID := keyedStore(t)
	hook, err := s.AddWebhook(repoID, "https://hook.example/x", "hook-secret", "*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO events (repo_id, kind, data_json) VALUES (?, 'push', '{}')", repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO webhook_deliveries (webhook_id, event_id) SELECT ?, MAX(id) FROM events", hook); err != nil {
		t.Fatal(err)
	}
	dd, err := s.DueDeliveries(10)
	if err != nil || len(dd) != 1 || dd[0].Secret != "hook-secret" {
		t.Fatalf("DueDeliveries = %+v, %v", dd, err)
	}

	if _, err := s.AddPushDevice(uid, "apns-token", "phone"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePush(uid, "t", "b", "/p"); err != nil {
		t.Fatal(err)
	}
	qp, err := s.DuePush(10)
	if err != nil || len(qp) != 1 || qp[0].Token != "apns-token" {
		t.Fatalf("DuePush = %+v, %v", qp, err)
	}
}

// A sealed value copied into another row of its column does not open:
// the additional data names the row as well as the column.
func TestSealedValueBoundToRow(t *testing.T) {
	s, _, uid, repoID := keyedStore(t)
	other, err := s.CreateRepo("user", uid, "other", "public")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetBuildSecret(repoID, "A", "a"))
	must(s.SetBuildSecret(repoID, "B", "b"))
	must(s.SetBuildSecret(other, "A", "c"))
	_, err = s.AddWebhook(repoID, "https://hook.example/1", "s1", "*")
	must(err)
	_, err = s.AddWebhook(repoID, "https://hook.example/2", "s2", "*")
	must(err)
	_, err = s.AddMirror(repoID, "push", "https://m.example/1.git", "", "t1")
	must(err)
	_, err = s.AddMirror(repoID, "pull", "https://m.example/2.git", "", "t2")
	must(err)
	_, err = s.AddPushDevice(uid, "d1", "")
	must(err)
	_, err = s.AddPushDevice(bob, "d2", "")
	must(err)

	// push_devices.token is unique, so alice's row goes before her
	// sealed token is copied into bob's.
	var aliceTok string
	must(s.DB.QueryRow("SELECT token FROM push_devices WHERE user_id = ?", uid).Scan(&aliceTok))
	_, err = s.DB.Exec("DELETE FROM push_devices WHERE user_id = ?", uid)
	must(err)

	cases := []struct {
		name string
		copy string
		read func() error
	}{
		{"build secret to another name",
			"UPDATE build_secrets SET value = (SELECT value FROM build_secrets WHERE repo_id = ?1 AND name = 'A') WHERE repo_id = ?1 AND name = 'B'",
			func() error { _, err := s.BuildSecrets(repoID); return err }},
		{"build secret to another repository",
			"UPDATE build_secrets SET value = (SELECT value FROM build_secrets WHERE repo_id = ?1 AND name = 'A') WHERE repo_id = ?2 AND name = 'A'",
			func() error { _, err := s.BuildSecrets(other); return err }},
		{"webhook secret",
			"UPDATE webhooks SET secret = (SELECT secret FROM webhooks WHERE url LIKE '%/1') WHERE url LIKE '%/2'",
			func() error { _, err := s.ListWebhooks(repoID); return err }},
		{"mirror token",
			"UPDATE mirrors SET token = (SELECT token FROM mirrors WHERE direction = 'push') WHERE direction = 'pull'",
			func() error { _, err := s.ListMirrors(repoID); return err }},
		{"push token",
			"UPDATE push_devices SET token = ?5 WHERE user_id = ?4",
			func() error { _, err := s.PushDevices(bob); return err }},
	}
	for _, c := range cases {
		if _, err := s.DB.Exec(c.copy, repoID, other, uid, bob, aliceTok); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := c.read(); err == nil {
			t.Errorf("%s: a value copied from another row opened", c.name)
		}
	}
}

// A token re-registered under another account changes hands by its
// hash, since two seals of one token differ.
func TestPushDeviceUpsertBySealedToken(t *testing.T) {
	s, _, uid, _ := keyedStore(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AddPushDevice(uid, "tok", "phone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddPushDevice(bob, "tok", "ipad")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("re-registration made row %d beside %d", second, first)
	}
	d, err := s.PushDevices(bob)
	if err != nil || len(d) != 1 || d[0].Token != "tok" {
		t.Fatalf("PushDevices after handover = %+v, %v", d, err)
	}
	if err := s.DeletePushDeviceByToken("tok"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.PushDevices(bob); len(d) != 0 {
		t.Fatalf("device left after delete by token: %+v", d)
	}
}

// A device row from before token_hash existed is found by its clear
// token until ResealSecrets fills the hash.
func TestPushDeviceUnhashedRow(t *testing.T) {
	s, _, uid, _ := keyedStore(t)
	if _, err := s.DB.Exec("INSERT INTO push_devices (user_id, token, label) VALUES (?, 'old', '')", uid); err != nil {
		t.Fatal(err)
	}
	var first int64
	s.DB.QueryRow("SELECT id FROM push_devices").Scan(&first)
	id, err := s.AddPushDevice(uid, "old", "phone")
	if err != nil || id != first {
		t.Fatalf("AddPushDevice = %d, %v; want row %d", id, err, first)
	}
	if _, err := s.DB.Exec("INSERT INTO push_devices (user_id, token, label) VALUES (?, 'older', '')", uid); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePushDeviceByToken("older"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 1 || d[0].Token != "old" {
		t.Fatalf("PushDevices = %+v", d)
	}
}

// Rows written before sealing existed, and rows under a retired key,
// end up under the current key.
func TestResealSecrets(t *testing.T) {
	s, path, uid, repoID := keyedStore(t)
	if _, err := s.DB.Exec("INSERT INTO build_secrets (repo_id, name, value) VALUES (?, 'OLD', 'clear-value')", repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO push_devices (user_id, token, label) VALUES (?, 'clear-token', '')", uid); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResealSecrets()
	if err != nil || n != 2 {
		t.Fatalf("ResealSecrets = %d, %v; want 2", n, err)
	}
	for _, v := range raw(t, s) {
		if !seal.IsSealed(v) {
			t.Errorf("still clear: %q", v)
		}
	}
	var hash string
	s.DB.QueryRow("SELECT COALESCE(token_hash, '') FROM push_devices").Scan(&hash)
	if hash != tokenHash("clear-token") {
		t.Errorf("token_hash = %q", hash)
	}
	if d, err := s.PushDevices(uid); err != nil || len(d) != 1 || d[0].Token != "clear-token" {
		t.Fatalf("PushDevices after reseal = %+v, %v", d, err)
	}
	if n, _ := s.ResealSecrets(); n != 0 {
		t.Errorf("second reseal rewrote %d values", n)
	}

	// Rotation: add a key, reseal, drop the old key; the value still opens.
	old, err := seal.ReadKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := seal.NewKey()
	if err := seal.WriteKeys(path, append(old, next)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResealSecrets(); err != nil || n != 2 {
		t.Fatalf("reseal after rotation = %d, %v; want 2", n, err)
	}
	if err := seal.WriteKeys(path, []seal.Key{next}); err != nil {
		t.Fatal(err)
	}
	use, err := s.SecretKeyUse()
	if err != nil || use[next.ID] != 2 || len(use) != 1 {
		t.Fatalf("SecretKeyUse = %v, %v", use, err)
	}
	if got, _ := s.BuildSecrets(repoID); got["OLD"] != "clear-value" {
		t.Fatalf("value after rotation: %v", got)
	}
}

func TestSealedValueWithoutKeyFails(t *testing.T) {
	s, _, _, repoID := keyedStore(t)
	if err := s.SetBuildSecret(repoID, "X", "v"); err != nil {
		t.Fatal(err)
	}
	s.SetKeyring(nil)
	if _, err := s.BuildSecrets(repoID); err == nil {
		t.Fatal("opened a sealed value with no key loaded")
	}
}
