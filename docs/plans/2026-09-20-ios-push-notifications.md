# iOS push notifications — server half — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** gitbayd delivers activity notices to registered Apple devices over APNs, as a third route beside the inbox row and the activity mail `notify()` already sends.

**Architecture:** A notice becomes one `push_queue` row per registered device. An `internal/push.Deliverer` drains the queue on a ticker and POSTs each row to APNs over HTTP/2, authenticated by an ES256 JWT signed with an operator-supplied `.p8`. This is the third instance of a shape the repository already has twice: `internal/notify` (mail) and `internal/webhook` (HTTP POSTs) — a queue table, a drainer goroutine, exponential backoff, dead-lettering.

**Tech Stack:** Go 1.27, SQLite (hand-written SQL, no ORM), stdlib only. No new module dependencies: `net/http` negotiates HTTP/2 over ALPN, and the JWT is `crypto/ecdsa` plus `encoding/json`.

**Spec:** `docs/specs/2026-09-20-ios-push-notifications-design.md`

## Global Constraints

- **No new Go module dependencies.** Nothing is added to `go.mod`. APNs needs HTTP/2, which stdlib `net/http` does over ALPN. The JWT is hand-rolled; do not reach for a JWT library.
- **Never attribute anything to an assistant or model.** Not in commits, not in code comments, not in MR bodies, not in docs.
- **Never push to `main`.** All work is on the `ios-push` branch in the worktree `/Users/cmc/git/krz/gitbay-push`. `require_mr` is on for this repository — a direct push to `main` is refused in pre-receive.
- **Commits must be signed.** This repository refuses unsigned commits. Use `git -c commit.gpgsign=true commit`.
- **Commit messages reference the issue:** `Ref #89`, and `Closes #89` on the last one.
- **Secrets on stdin, never argv.** `/proc` is world-readable.
- **A command that reads stdin must set `ReadsStdin: true`** on its `Command`. Otherwise `control.go` swaps in an empty reader and `--file -` silently stores nothing — it does not error.
- **A new control command needs a `pass()` entry** in `cmd/gitbay/main.go` or the CLI coverage test fails.
- **A new page template needs a row in `TestMainWidthClass`** (`internal/web/web_test.go`) or CI fails on it.
- **Test scope while working:** build, `go vet ./...`, and the unit tests of the packages you touched. Full `go test ./...` belongs to CI on bay1 — the e2e suite is most of the runtime. Run `go vet ./...` after any signature change; `go build` skips `_test.go` files and will not catch a stale test caller.
- **Never define a color only inside the dark media query** (relevant only to Task 10).

---

### Task 1: Migration 0059 and the device table

**Files:**
- Create: `internal/store/migrations/0059_push.up.sql`
- Create: `internal/store/migrations/0059_push.down.sql`
- Create: `internal/store/push.go`
- Test: `internal/store/push_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type PushDevice struct { ID int64; UserID int64; Token string; Label string; CreatedAt string; LastSeenAt string }`
  - `func (s *Store) AddPushDevice(userID int64, token, label string) (int64, error)`
  - `func (s *Store) PushDevices(userID int64) ([]PushDevice, error)`
  - `func (s *Store) RemovePushDevice(userID, id int64) error`
  - `func (s *Store) PushEnabled(userID int64) (bool, error)`
  - `func (s *Store) SetPushEnabled(userID int64, on bool) error`

- [ ] **Step 1: Write the migration**

`internal/store/migrations/0059_push.up.sql`:

```sql
-- Apple devices an account has registered, and the queue of pushes bound
-- for them. The mail queue's table is named `notifications`, so this one
-- cannot be; the columns mirror it so the drainer is the mailer's loop.
CREATE TABLE push_devices (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    label        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen_at TEXT
);
CREATE INDEX push_devices_user ON push_devices(user_id);

CREATE TABLE push_queue (
    id              INTEGER PRIMARY KEY,
    device_id       INTEGER NOT NULL REFERENCES push_devices(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    body            TEXT NOT NULL,
    path            TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    sent_at         TEXT,
    failed_at       TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX push_queue_due ON push_queue(next_attempt_at)
    WHERE sent_at IS NULL AND failed_at IS NULL;

-- Whether activity reaches the account's registered devices. Defaults on
-- and costs nothing for an account with no devices; it exists so a user
-- with a phone and an iPad silences both without deregistering each.
ALTER TABLE users ADD COLUMN notify_push INTEGER NOT NULL DEFAULT 1;
```

`internal/store/migrations/0059_push.down.sql`:

```sql
DROP TABLE push_queue;
DROP TABLE push_devices;
ALTER TABLE users DROP COLUMN notify_push;
```

- [ ] **Step 2: Write the failing test**

`internal/store/push_test.go`:

```go
package store

import "testing"

func TestPushDevices(t *testing.T) {
	s := testStore(t)
	uid := testUser(t, s, "alice")

	id, err := s.AddPushDevice(uid, "tok-a", "iphone")
	if err != nil {
		t.Fatalf("AddPushDevice: %v", err)
	}
	devices, err := s.PushDevices(uid)
	if err != nil {
		t.Fatalf("PushDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].Token != "tok-a" || devices[0].Label != "iphone" {
		t.Fatalf("got %+v", devices)
	}

	// Apple reuses tokens: re-registering updates the label and the owner
	// rather than erroring, so a reinstall under another account works.
	bob := testUser(t, s, "bob")
	if _, err := s.AddPushDevice(bob, "tok-a", "ipad"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 0 {
		t.Fatalf("token still owned by alice: %+v", d)
	}
	d, _ := s.PushDevices(bob)
	if len(d) != 1 || d[0].Label != "ipad" {
		t.Fatalf("got %+v", d)
	}

	// Removal is scoped to the owner: alice cannot remove bob's device.
	if err := s.RemovePushDevice(uid, d[0].ID); err != ErrNotFound {
		t.Fatalf("cross-account remove: got %v, want ErrNotFound", err)
	}
	if err := s.RemovePushDevice(bob, d[0].ID); err != nil {
		t.Fatalf("RemovePushDevice: %v", err)
	}
	if d, _ := s.PushDevices(bob); len(d) != 0 {
		t.Fatalf("device survived removal: %+v", d)
	}
	_ = id
}

func TestPushEnabledDefaultsOn(t *testing.T) {
	s := testStore(t)
	uid := testUser(t, s, "alice")
	on, err := s.PushEnabled(uid)
	if err != nil {
		t.Fatalf("PushEnabled: %v", err)
	}
	if !on {
		t.Fatal("notify_push should default on")
	}
	if err := s.SetPushEnabled(uid, false); err != nil {
		t.Fatalf("SetPushEnabled: %v", err)
	}
	if on, _ := s.PushEnabled(uid); on {
		t.Fatal("SetPushEnabled(false) did not stick")
	}
}
```

Check the helper names `testStore` and `testUser` against the existing
`internal/store/inbox_test.go` and use whatever that file uses; do not
invent new helpers.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/store/ -run 'TestPush' -v`
Expected: FAIL — `s.AddPushDevice undefined`.

- [ ] **Step 4: Write the implementation**

`internal/store/push.go`:

```go
package store

import (
	"database/sql"
	"errors"
)

// PushDevice is one Apple device an account has registered. Token is the
// APNs device token: an address, not a credential, but device-identifying
// and never logged or echoed in full.
type PushDevice struct {
	ID         int64
	UserID     int64
	Token      string
	Label      string
	CreatedAt  string
	LastSeenAt string
}

// AddPushDevice registers a token to an account. A token already present
// changes hands rather than erroring: Apple reuses tokens, and a reinstall
// hands the same one to whichever account signs in next.
func (s *Store) AddPushDevice(userID int64, token, label string) (int64, error) {
	res, err := s.DB.Exec(`
		INSERT INTO push_devices (user_id, token, label) VALUES (?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET user_id = excluded.user_id, label = excluded.label`,
		userID, token, label)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) PushDevices(userID int64) ([]PushDevice, error) {
	rows, err := s.DB.Query(`
		SELECT id, user_id, token, label, created_at, COALESCE(last_seen_at, '')
		FROM push_devices WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushDevice
	for rows.Next() {
		var d PushDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.Token, &d.Label, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemovePushDevice deletes one of the account's own devices. Scoping the
// delete by user_id rather than checking ownership first means another
// account's id is ErrNotFound, which is the same answer as an id that
// never existed — a caller learns nothing about other accounts' devices.
func (s *Store) RemovePushDevice(userID, id int64) error {
	res, err := s.DB.Exec("DELETE FROM push_devices WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PushEnabled(userID int64) (bool, error) {
	var on int
	err := s.DB.QueryRow("SELECT notify_push FROM users WHERE id = ?", userID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return on != 0, err
}

func (s *Store) SetPushEnabled(userID int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.DB.Exec("UPDATE users SET notify_push = ? WHERE id = ?", v, userID)
	return err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run 'TestPush' -v`
Expected: PASS, both tests.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0059_push.up.sql internal/store/migrations/0059_push.down.sql internal/store/push.go internal/store/push_test.go
git -c commit.gpgsign=true commit -m "store: push device registrations

Migration 0059 adds push_devices, push_queue and users.notify_push. A
re-registered token changes hands rather than erroring, since Apple
reuses tokens across reinstalls.

Ref #89"
```

---

### Task 2: The push queue and its retention

**Files:**
- Modify: `internal/store/push.go`
- Modify: `internal/store/retention.go:30-35` (the `Retention` struct) and the `aged` table around `:66-75`
- Modify: `internal/config/config.go` (the `Retention` struct and its `Durations` method)
- Modify: `cmd/gitbayd/main.go:518-520`
- Test: `internal/store/push_test.go`

**Interfaces:**
- Consumes: `PushDevice`, `PushEnabled` from Task 1.
- Produces:
  - `type QueuedPush struct { ID int64; DeviceID int64; Token string; Title string; Body string; Path string; Attempts int }`
  - `func (s *Store) EnqueuePush(userID int64, title, body, path string) error`
  - `func (s *Store) DuePush(limit int) ([]QueuedPush, error)`
  - `func (s *Store) MarkPushSent(id int64) error`
  - `func (s *Store) MarkPushFailed(id int64, errMsg string, nextAt *time.Time) error`
  - `func (s *Store) DeletePushDeviceByToken(token string) error`
  - `config.Retention.Push string` with toml key `push`, and a fifth return from `Durations()`
  - `store.Retention.Push time.Duration`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/push_test.go`:

```go
func TestEnqueuePush(t *testing.T) {
	s := testStore(t)
	uid := testUser(t, s, "alice")
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.AddPushDevice(uid, "tok-b", "ipad")

	// One row per device, so a retry to the phone does not resend to the
	// iPad.
	if err := s.EnqueuePush(uid, "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12"); err != nil {
		t.Fatalf("EnqueuePush: %v", err)
	}
	due, err := s.DuePush(20)
	if err != nil {
		t.Fatalf("DuePush: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("want a row per device, got %d", len(due))
	}
	if due[0].Token == "" || due[0].Body != "cmc opened issue #12" {
		t.Fatalf("got %+v", due[0])
	}

	// Sent rows stop being due.
	if err := s.MarkPushSent(due[0].ID); err != nil {
		t.Fatalf("MarkPushSent: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 1 {
		t.Fatalf("sent row still due")
	}

	// A failure with a next attempt in the future is not due yet.
	next := time.Now().Add(time.Hour)
	if err := s.MarkPushFailed(due[1].ID, "503", &next); err != nil {
		t.Fatalf("MarkPushFailed: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("backed-off row is due too early")
	}
}

func TestEnqueuePushRespectsSettingAndDevices(t *testing.T) {
	s := testStore(t)
	uid := testUser(t, s, "alice")

	// No devices: nothing queued, no error.
	if err := s.EnqueuePush(uid, "t", "b", "p"); err != nil {
		t.Fatalf("EnqueuePush with no devices: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued for an account with no devices")
	}

	// Setting off: nothing queued.
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.SetPushEnabled(uid, false)
	if err := s.EnqueuePush(uid, "t", "b", "p"); err != nil {
		t.Fatalf("EnqueuePush with push off: %v", err)
	}
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued with notify_push off")
	}
}

func TestDeletePushDeviceByTokenTakesItsQueue(t *testing.T) {
	s := testStore(t)
	uid := testUser(t, s, "alice")
	s.AddPushDevice(uid, "tok-a", "iphone")
	s.EnqueuePush(uid, "t", "b", "p")

	if err := s.DeletePushDeviceByToken("tok-a"); err != nil {
		t.Fatalf("DeletePushDeviceByToken: %v", err)
	}
	if d, _ := s.PushDevices(uid); len(d) != 0 {
		t.Fatalf("device survived")
	}
	// push_queue.device_id is ON DELETE CASCADE, so the queued rows go
	// with it rather than being retried at a dead token forever.
	if due, _ := s.DuePush(20); len(due) != 0 {
		t.Fatalf("queued rows outlived their device")
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run 'TestEnqueuePush|TestDeletePushDevice' -v`
Expected: FAIL — `s.EnqueuePush undefined`.

- [ ] **Step 3: Write the queue implementation**

Append to `internal/store/push.go` (and add `"time"` to its imports):

```go
// QueuedPush is one pending push, joined to the token it is bound for so
// the drainer needs one query rather than two.
type QueuedPush struct {
	ID       int64
	DeviceID int64
	Token    string
	Title    string
	Body     string
	Path     string
	Attempts int
}

// EnqueuePush writes one row per registered device, and nothing when the
// account has push off or no devices — the same shape as
// ActivityMailAddress returning "" when notify_mail is off. Mute, watch
// and actor-exclusion are already settled by NotifyRecipients before a
// caller reaches here.
func (s *Store) EnqueuePush(userID int64, title, body, path string) error {
	on, err := s.PushEnabled(userID)
	if err != nil || !on {
		return err
	}
	_, err = s.DB.Exec(`
		INSERT INTO push_queue (device_id, title, body, path)
		SELECT id, ?, ?, ? FROM push_devices WHERE user_id = ?`,
		title, body, path, userID)
	return err
}

func (s *Store) DuePush(limit int) ([]QueuedPush, error) {
	rows, err := s.DB.Query(`
		SELECT q.id, q.device_id, d.token, q.title, q.body, q.path, q.attempts
		FROM push_queue q JOIN push_devices d ON d.id = q.device_id
		WHERE q.sent_at IS NULL AND q.failed_at IS NULL
		  AND (q.next_attempt_at IS NULL OR q.next_attempt_at <= ?)
		ORDER BY q.id LIMIT ?`, fmtTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedPush
	for rows.Next() {
		var p QueuedPush
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.Token, &p.Title, &p.Body, &p.Path, &p.Attempts); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) MarkPushSent(id int64) error {
	_, err := s.DB.Exec(
		"UPDATE push_queue SET sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1 WHERE id = ?", id)
	return err
}

func (s *Store) MarkPushFailed(id int64, errMsg string, nextAt *time.Time) error {
	if nextAt == nil {
		_, err := s.DB.Exec(
			"UPDATE push_queue SET failed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1, last_error = ? WHERE id = ?",
			errMsg, id)
		return err
	}
	_, err := s.DB.Exec(
		"UPDATE push_queue SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?",
		errMsg, fmtTime(*nextAt), id)
	return err
}

// DeletePushDeviceByToken drops a device Apple has told us is gone. The
// queue rows cascade, so nothing is left retrying at a dead token.
func (s *Store) DeletePushDeviceByToken(token string) error {
	_, err := s.DB.Exec("DELETE FROM push_devices WHERE token = ?", token)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -run 'TestPush|TestEnqueuePush|TestDeletePushDevice' -v`
Expected: PASS.

If `TestDeletePushDeviceByTokenTakesItsQueue` fails with the queue rows
surviving, foreign keys are not on for that connection. Check how
`testStore` opens the database against the rest of `internal/store` —
do not work around it by deleting the queue rows by hand.

- [ ] **Step 5: Add the retention key**

In `internal/config/config.go`, add to the `Retention` struct:

```go
	// Push is the outbound device queue: rows already sent or given up on.
	Push string `toml:"push"`
```

Find `Retention.Durations()` in the same file and give it a fifth return
value parsed the same way as `Mail`.

In `internal/store/retention.go`, add to the `Retention` struct:

```go
	Push              time.Duration
```

and to the `aged` slice in `Sweep`, after the `notifications` row:

```go
		{"push_queue", "created_at < ? AND (sent_at IS NOT NULL OR failed_at IS NOT NULL)", r.Push},
```

In `cmd/gitbayd/main.go`, the `sweep` function around line 518:

```go
	audit, events, deliveries, mail, push := cfg.Retention.Durations()
	r := store.Retention{Audit: audit, Events: events,
		WebhookDeliveries: deliveries, Mail: mail, Push: push}
```

- [ ] **Step 6: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: clean. `Durations()` gained a return value, so `go vet` is what
catches any caller `go build` skipped — check for callers in
`internal/config`'s own tests.

Run: `go test ./internal/config/ ./internal/store/ ./cmd/gitbayd/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/push.go internal/store/push_test.go internal/store/retention.go internal/config/config.go cmd/gitbayd/main.go
git -c commit.gpgsign=true commit -m "store: the push queue, swept like the mail queue

One row per device per notice, so a retry to one device does not
resend to another. EnqueuePush writes nothing when the account has
push off or no devices. [retention] push caps the table.

Ref #89"
```

---

### Task 3: The `[push]` config section

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Push struct { Enabled bool; KeyFile string; KeyID string; TeamID string; Topic string; Environment string }` with toml keys `enabled`, `key_file`, `key_id`, `team_id`, `topic`, `environment`
  - `Config.Push Push` with toml key `push`
  - `func (p Push) Host() string` returning `api.push.apple.com` or `api.sandbox.push.apple.com`, overridden by `GITBAY_APNS_HOST`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`. It already has
`writeConfig(t, body) string` and a `minimal` constant; use both rather
than adding a second way to load a config.

```go
// writeP8 writes a PEM-wrapped PKCS#8 P-256 key, the shape of Apple's
// .p8 provider key, and returns its path.
func writeP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "apns.p8")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPushConfigValidation(t *testing.T) {
	keyPath := writeP8(t)
	full := `
[push]
enabled = true
key_file = "` + keyPath + `"
key_id = "KEYID"
team_id = "TEAMID"
topic = "org.gitbay.gitbay"
environment = "production"
`
	cases := []struct {
		name string
		body string
		want string // substring of the expected error; "" means valid
	}{
		{"disabled needs nothing", "\n[push]\nenabled = false\n", ""},
		{"complete is valid", full, ""},
		{"key_id required", strings.Replace(full, `key_id = "KEYID"`, "", 1), "push.key_id"},
		{"team_id required", strings.Replace(full, `team_id = "TEAMID"`, "", 1), "push.team_id"},
		{"topic required", strings.Replace(full, `topic = "org.gitbay.gitbay"`, "", 1), "push.topic"},
		{"environment must be a known name",
			strings.Replace(full, `environment = "production"`, `environment = "staging"`, 1),
			"push.environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, minimal+tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// A key_file that exists but is not a PKCS#8 EC key is refused at load,
// not at the first notice: the failure mode otherwise is a queue that
// fills and dead-letters with nobody watching.
func TestPushConfigRejectsAnUnparseableKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.p8")
	if err := os.WriteFile(p, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `
[push]
enabled = true
key_file = "` + p + `"
key_id = "K"
team_id = "T"
topic = "org.gitbay.gitbay"
environment = "production"
`
	_, err := Load(writeConfig(t, minimal+body))
	if err == nil || !strings.Contains(err.Error(), "push.key_file") {
		t.Fatalf("want a push.key_file error, got %v", err)
	}
}

func TestPushHost(t *testing.T) {
	if got := (Push{Environment: "production"}).Host(); got != "api.push.apple.com" {
		t.Fatalf("production host = %q", got)
	}
	if got := (Push{Environment: "sandbox"}).Host(); got != "api.sandbox.push.apple.com" {
		t.Fatalf("sandbox host = %q", got)
	}
	t.Setenv("GITBAY_APNS_HOST", "127.0.0.1:1234")
	if got := (Push{Environment: "production"}).Host(); got != "127.0.0.1:1234" {
		t.Fatalf("GITBAY_APNS_HOST ignored: %q", got)
	}
}
```

Add `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/x509` and
`encoding/pem` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestPush -v`
Expected: FAIL — `Push` undefined.

- [ ] **Step 3: Write the implementation**

Add to `internal/config/config.go`, beside the other section structs:

```go
// Push is APNs delivery to registered Apple devices. A key belongs to a
// bundle ID, so an instance pushes to the app built under the topic named
// here and no other; a self-hoster points this at their own key and their
// own build.
type Push struct {
	Enabled  bool   `toml:"enabled"`
	KeyFile  string `toml:"key_file"`
	KeyID    string `toml:"key_id"`
	TeamID   string `toml:"team_id"`
	Topic    string `toml:"topic"` // the app's bundle identifier
	// Environment is a name rather than a URL so a typo cannot aim the
	// key at a host that is not Apple's.
	Environment string `toml:"environment"` // production | sandbox
}

// Host is the APNs endpoint for the configured environment.
// GITBAY_APNS_HOST overrides it for tests, as GITBAY_SWEEP_TICK does for
// the retention sweep.
func (p Push) Host() string {
	if h := os.Getenv("GITBAY_APNS_HOST"); h != "" {
		return h
	}
	if p.Environment == "sandbox" {
		return "api.sandbox.push.apple.com"
	}
	return "api.push.apple.com"
}
```

Add the field to `Config`:

```go
	Push         Push         `toml:"push"`
```

In the validate function, beside the `MaxSnippetsPerUser` check:

```go
	if c.Push.Enabled {
		for _, f := range []struct{ name, val string }{
			{"push.key_file", c.Push.KeyFile},
			{"push.key_id", c.Push.KeyID},
			{"push.team_id", c.Push.TeamID},
			{"push.topic", c.Push.Topic},
		} {
			if f.val == "" {
				errs = append(errs, fmt.Errorf("%s is required when push.enabled", f.name))
			}
		}
		if err := oneOf("push.environment", c.Push.Environment, "production", "sandbox"); err != nil {
			errs = append(errs, err)
		}
		if c.Push.KeyFile != "" {
			if _, err := LoadAPNSKey(c.Push.KeyFile); err != nil {
				errs = append(errs, fmt.Errorf("push.key_file: %w", err))
			}
		}
	}
```

And the key loader, in the same file:

```go
// LoadAPNSKey reads Apple's .p8 provider key: a PEM-wrapped PKCS#8
// P-256 private key. Read at startup and validated there, so a
// misconfigured [push] refuses to start rather than filling a queue
// nobody is watching.
func LoadAPNSKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := any.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC private key")
	}
	return key, nil
}
```

Add `crypto/ecdsa`, `crypto/x509` and `encoding/pem` to the file's imports.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS. The whole package, because adding a `Config` field can
break a test that round-trips the struct or asserts on unknown keys.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git -c commit.gpgsign=true commit -m "config: the [push] section

Validated at load: with push.enabled, the four fields are required,
environment is one of two names, and key_file must parse as a PKCS#8
EC key. GITBAY_APNS_HOST redirects the endpoint for tests.

Ref #89"
```

---

### Task 4: The APNs provider token

**Files:**
- Create: `internal/push/token.go`
- Test: `internal/push/token_test.go`

**Interfaces:**
- Consumes: `config.Push`, `config.LoadAPNSKey` from Task 3.
- Produces:
  - `type tokenSource struct { key *ecdsa.PrivateKey; keyID, teamID string; now func() time.Time; mu sync.Mutex; cached string; issued time.Time }`
  - `func newTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *tokenSource`
  - `func (t *tokenSource) token() (string, error)`

- [ ] **Step 1: Write the failing test**

`internal/push/token_test.go`:

```go
package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTokenShapeAndSignature(t *testing.T) {
	key := testKey(t)
	ts := newTokenSource(key, "KEYID123", "TEAMID456")
	tok, err := ts.token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want three dot-separated parts, got %d", len(parts))
	}

	var hdr struct{ Alg, Kid string }
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("header: %v", err)
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEYID123" {
		t.Fatalf("header = %+v", hdr)
	}

	// APNs provider tokens carry iss (team id) and iat, and nothing else.
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims["iss"] != "TEAMID456" {
		t.Fatalf("iss = %v", claims["iss"])
	}
	if _, ok := claims["iat"]; !ok {
		t.Fatal("no iat")
	}
	if len(claims) != 2 {
		t.Fatalf("unexpected claims: %v", claims)
	}

	// The signature is raw r||s, 64 bytes — not the ASN.1 DER that
	// ecdsa.SignASN1 returns. Sending DER gets every push rejected.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature not base64url: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want 64 (raw r||s)", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Fatal("signature does not verify")
	}
}

func TestTokenCachedThenReminted(t *testing.T) {
	ts := newTokenSource(testKey(t), "K", "T")
	base := time.Now()
	ts.now = func() time.Time { return base }

	first, _ := ts.token()
	second, _ := ts.token()
	if first != second {
		t.Fatal("token reminted inside the cache window; APNs answers TooManyProviderTokenUpdates")
	}

	// Valid for an hour, not to be reminted faster than every twenty
	// minutes: refresh at fifty.
	ts.now = func() time.Time { return base.Add(51 * time.Minute) }
	third, _ := ts.token()
	if third == first {
		t.Fatal("token not reminted after fifty minutes")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/push/ -run TestToken -v`
Expected: FAIL — `newTokenSource` undefined.

- [ ] **Step 3: Write the implementation**

`internal/push/token.go`:

```go
// Package push delivers activity notices to Apple devices over APNs: the
// third delivery route beside the inbox row and the activity mail, with
// the bounded-retry discipline the mail queue and webhook deliverer use.
package push

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

// tokenLifetime is how long a provider token is reused. APNs accepts one
// for an hour and answers TooManyProviderTokenUpdates if they are minted
// faster than roughly once every twenty minutes, so the useful window is
// between the two.
const tokenLifetime = 50 * time.Minute

type tokenSource struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	now    func() time.Time

	mu     sync.Mutex
	cached string
	issued time.Time
}

func newTokenSource(key *ecdsa.PrivateKey, keyID, teamID string) *tokenSource {
	return &tokenSource{key: key, keyID: keyID, teamID: teamID, now: time.Now}
}

// token returns the cached provider token, minting a new one when the old
// one is near its end.
func (t *tokenSource) token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.cached != "" && now.Sub(t.issued) < tokenLifetime {
		return t.cached, nil
	}
	tok, err := t.sign(now)
	if err != nil {
		return "", err
	}
	t.cached, t.issued = tok, now
	return tok, nil
}

func (t *tokenSource) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": t.keyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": t.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, t.key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS wants the raw pair, each left-padded to the curve's byte size —
	// not ecdsa.SignASN1's DER. A DER signature is well-formed ECDSA and
	// is rejected by every JWT verifier, APNs included.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/push/ -run TestToken -v`
Expected: PASS, both tests.

- [ ] **Step 5: Commit**

```bash
git add internal/push/token.go internal/push/token_test.go
git -c commit.gpgsign=true commit -m "push: APNs provider tokens

ES256 over iss and iat, cached fifty minutes. The signature is raw
r||s rather than DER, which is the difference between a token APNs
accepts and one it rejects.

Ref #89"
```

---

### Task 5: The APNs client

**Files:**
- Create: `internal/push/apns.go`
- Test: `internal/push/apns_test.go`

**Interfaces:**
- Consumes: `tokenSource` from Task 4, `config.Push` from Task 3.
- Produces:
  - `type Client struct { ... }`
  - `func NewClient(cfg config.Push) (*Client, error)`
  - `type result int` with constants `resultSent`, `resultRetry`, `resultReap`, `resultDead`
  - `func (c *Client) Send(ctx context.Context, token, title, body, path string) (res result, retryAfter time.Duration, err error)`

- [ ] **Step 1: Write the failing test**

`internal/push/apns_test.go`:

```go
package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"io"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// fakeAPNs stands in for Apple. It speaks HTTP/1.1; the real transport is
// h2 by ALPN, which is stdlib behaviour and not this repository's to test.
func fakeAPNs(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("GITBAY_APNS_HOST", strings.TrimPrefix(srv.URL, "http://"))
	c, err := NewClient(config.Push{
		Enabled: true, KeyID: "K", TeamID: "T",
		Topic: "org.gitbay.gitbay", Environment: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	c.key = testKey(t)
	c.tokens = newTokenSource(c.key, "K", "T")
	c.scheme = "http"
	return c, srv
}

func TestSendShapesTheRequest(t *testing.T) {
	var gotPath, gotTopic, gotType, gotAuth string
	var payload map[string]any
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTopic = r.URL.Path, r.Header.Get("apns-topic")
		gotType, gotAuth = r.Header.Get("apns-push-type"), r.Header.Get("authorization")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(200)
	})
	res, _, err := c.Send(context.Background(), "DEVTOKEN", "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12")
	if err != nil || res != resultSent {
		t.Fatalf("res = %v, err = %v", res, err)
	}
	if gotPath != "/3/device/DEVTOKEN" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotTopic != "org.gitbay.gitbay" || gotType != "alert" {
		t.Fatalf("topic = %q, push-type = %q", gotTopic, gotType)
	}
	if !strings.HasPrefix(gotAuth, "bearer ") {
		t.Fatalf("authorization = %q", gotAuth)
	}
	aps := payload["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "krz/gitbay" || alert["body"] != "cmc opened issue #12" {
		t.Fatalf("alert = %v", alert)
	}
	if aps["thread-id"] != "krz/gitbay" {
		t.Fatalf("thread-id = %v", aps["thread-id"])
	}
	if payload["path"] != "krz/gitbay/issues/12" {
		t.Fatalf("path = %v", payload["path"])
	}
	// Collapsing is wrong here: two comments are two notices.
	if _, ok := payload["apns-collapse-id"]; ok {
		t.Fatal("collapse id set")
	}
}

func TestSendMapsResponses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		want       result
		wantAfter  time.Duration
	}{
		{"ok", 200, "", "", resultSent, 0},
		{"gone", 410, `{"reason":"Unregistered"}`, "", resultReap, 0},
		{"bad token", 400, `{"reason":"BadDeviceToken"}`, "", resultReap, 0},
		{"other 400 is permanent", 400, `{"reason":"PayloadTooLarge"}`, "", resultDead, 0},
		{"forbidden is permanent", 403, `{"reason":"InvalidProviderToken"}`, "", resultDead, 0},
		{"too many requests retries", 429, `{"reason":"TooManyRequests"}`, "7", resultRetry, 7 * time.Second},
		{"server error retries", 503, `{"reason":"ServiceUnavailable"}`, "", resultRetry, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			res, after, err := c.Send(context.Background(), "T", "t", "b", "p")
			if err != nil && tc.want != resultDead && tc.want != resultReap {
				t.Fatalf("err = %v", err)
			}
			if res != tc.want {
				t.Fatalf("res = %v, want %v", res, tc.want)
			}
			if after != tc.wantAfter {
				t.Fatalf("retryAfter = %v, want %v", after, tc.wantAfter)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/push/ -run TestSend -v`
Expected: FAIL — `NewClient` undefined.

- [ ] **Step 3: Write the implementation**

`internal/push/apns.go`:

```go
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// result is what one send means for the queue row.
type result int

const (
	resultSent  result = iota // delivered
	resultRetry               // transient; back off and try again
	resultReap                // Apple says the token is dead; drop the device
	resultDead                // permanent for this payload; dead-letter it
)

// maxBodyBytes keeps an alert inside APNs' 4KB payload limit with room
// for the rest of the JSON. A summary longer than this is cut rather
// than rejected.
const maxBodyBytes = 3000

type Client struct {
	http   *http.Client
	tokens *tokenSource
	key    *ecdsa.PrivateKey
	host   string
	scheme string
	topic  string
}

func NewClient(cfg config.Push) (*Client, error) {
	c := &Client{
		// stdlib negotiates HTTP/2 over ALPN, which is what APNs
		// requires; no explicit http2 transport is needed.
		http:   &http.Client{Timeout: 30 * time.Second},
		host:   cfg.Host(),
		scheme: "https",
		topic:  cfg.Topic,
	}
	if cfg.KeyFile != "" {
		key, err := config.LoadAPNSKey(cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		c.key = key
		c.tokens = newTokenSource(key, cfg.KeyID, cfg.TeamID)
	}
	return c, nil
}
```

`c.tokens` is nil when `KeyFile` is empty, and `Send` would panic on it.
Production cannot reach that: config validation requires `key_file`
whenever `push.enabled`, and `Deliverer` is only started when it is. The
tests above set `c.tokens` themselves. Leave it rather than adding a nil
check that can only fire in a test that forgot one.

```go

// Send delivers one alert. The returned duration is the server's
// Retry-After when it gave one, zero otherwise.
func (c *Client) Send(ctx context.Context, token, title, body, path string) (result, time.Duration, error) {
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes]
	}
	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":     map[string]string{"title": title, "body": body},
			"sound":     "default",
			"thread-id": title,
		},
		"path": path,
	})
	if err != nil {
		return resultDead, 0, err
	}
	bearer, err := c.tokens.token()
	if err != nil {
		return resultRetry, 0, err
	}
	url := c.scheme + "://" + c.host + "/3/device/" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resultDead, 0, err
	}
	req.Header.Set("authorization", "bearer "+bearer)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return resultRetry, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var apnsErr struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(raw, &apnsErr)

	var after time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			after = time.Duration(n) * time.Second
		}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return resultSent, 0, nil
	case resp.StatusCode == http.StatusGone,
		apnsErr.Reason == "BadDeviceToken",
		apnsErr.Reason == "Unregistered":
		// Apple is authoritative about which tokens are live.
		return resultReap, 0, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return resultRetry, after, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	default:
		// Retrying a rejected payload will not fix it.
		return resultDead, 0, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/push/ -v`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/push/apns.go internal/push/apns_test.go
git -c commit.gpgsign=true commit -m "push: the APNs client

POSTs one alert per call and maps the response: 200 sent, 410 and
BadDeviceToken reap the device, 429 and 5xx retry honouring
Retry-After, everything else dead-letters.

Ref #89"
```

---

### Task 6: The drainer, and gitbayd wiring

**Files:**
- Create: `internal/push/push.go`
- Modify: `cmd/gitbayd/main.go:175-178`
- Test: `internal/push/push_test.go`

**Interfaces:**
- Consumes: `Client`, `result` constants from Task 5; the store queue functions from Task 2.
- Produces:
  - `type Deliverer struct { St *store.Store; Cl *Client; RetryBase time.Duration; MaxAttempts int }`
  - `func New(st *store.Store, cfg config.Push, retryBase time.Duration) (*Deliverer, error)`
  - `func (d *Deliverer) Run(ctx context.Context)`
  - `func (d *Deliverer) drain(ctx context.Context)` — one pass, for tests
  - `const DefaultMaxAttempts = 5`

- [ ] **Step 1: Write the failing test**

`internal/push/push_test.go`:

```go
package push

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestDrainSendsAndMarks(t *testing.T) {
	var hits int
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Millisecond, MaxAttempts: 5}

	d.drain(context.Background())

	if hits != 1 {
		t.Fatalf("sent %d times, want 1", hits)
	}
	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("row still due after a 200")
	}
}

func TestDrainReapsADeadToken(t *testing.T) {
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
		w.Write([]byte(`{"reason":"Unregistered"}`))
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Millisecond, MaxAttempts: 5}

	d.drain(context.Background())

	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("queue survived the reap")
	}
	// The device is gone, not merely its queue row.
	if n := countPushDevices(t, st); n != 0 {
		t.Fatalf("%d devices left after 410", n)
	}
}

func TestDrainBacksOffThenDeadLetters(t *testing.T) {
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	})
	st := testStoreWithQueuedPush(t, "tok-a")
	d := &Deliverer{St: st, Cl: c, RetryBase: time.Nanosecond, MaxAttempts: 3}

	// Three passes: two back off, the third gives up.
	for i := 0; i < 3; i++ {
		d.drain(context.Background())
	}
	if due, _ := st.DuePush(20); len(due) != 0 {
		t.Fatalf("row still due after MaxAttempts")
	}
	// A transient failure must not take the device with it.
	if n := countPushDevices(t, st); n != 1 {
		t.Fatalf("device reaped on a 503")
	}
}
```

Write `testStoreWithQueuedPush` and `countPushDevices` as helpers in this
file. `testStoreWithQueuedPush` opens a store the way `internal/store`'s
own tests do, creates a user, calls `AddPushDevice` and `EnqueuePush`,
and returns the store. If opening a store from `internal/push` is
awkward, put the helpers in `internal/store/export_test.go` style — check
what `internal/webhook`'s tests do for the same problem and follow it
rather than inventing a third way.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/push/ -run TestDrain -v`
Expected: FAIL — `Deliverer` undefined.

- [ ] **Step 3: Write the implementation**

`internal/push/push.go`:

```go
package push

import (
	"context"
	"log/slog"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// DefaultMaxAttempts matches the mailer's: a flaky APNs delays a
// notification rather than losing it, up to a point.
const DefaultMaxAttempts = 5

type Deliverer struct {
	St          *store.Store
	Cl          *Client
	RetryBase   time.Duration
	MaxAttempts int
}

func New(st *store.Store, cfg config.Push, retryBase time.Duration) (*Deliverer, error) {
	cl, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Deliverer{St: st, Cl: cl, RetryBase: retryBase, MaxAttempts: DefaultMaxAttempts}, nil
}

// Run drains the push queue until ctx is done.
func (d *Deliverer) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.drain(ctx)
		}
	}
}

func (d *Deliverer) drain(ctx context.Context) {
	due, err := d.St.DuePush(20)
	if err != nil {
		slog.Error("push: listing due", "err", err)
		return
	}
	for _, q := range due {
		res, after, sendErr := d.Cl.Send(ctx, q.Token, q.Title, q.Body, q.Path)
		msg := ""
		if sendErr != nil {
			msg = sendErr.Error()
		}
		switch res {
		case resultSent:
			d.St.MarkPushSent(q.ID)
		case resultReap:
			// The queued rows cascade with the device.
			if err := d.St.DeletePushDeviceByToken(q.Token); err != nil {
				slog.Error("push: reaping device", "device", q.DeviceID, "err", err)
			}
		case resultRetry:
			attempt := q.Attempts + 1
			if attempt >= d.MaxAttempts {
				d.St.MarkPushFailed(q.ID, msg, nil)
				// The device id, never the token.
				slog.Warn("push dead-lettered",
					"push", q.ID, "device", q.DeviceID, "attempts", attempt, "err", msg)
				continue
			}
			wait := after
			if wait == 0 {
				wait = d.RetryBase << (attempt - 1)
			}
			next := time.Now().Add(wait)
			d.St.MarkPushFailed(q.ID, msg, &next)
		default: // resultDead
			d.St.MarkPushFailed(q.ID, msg, nil)
			slog.Warn("push rejected", "push", q.ID, "device", q.DeviceID, "err", msg)
		}
	}
}
```

- [ ] **Step 4: Wire it into gitbayd**

In `cmd/gitbayd/main.go`, beside the mailer at line 177:

```go
			if cfg.Push.Enabled {
				p, err := push.New(st, cfg.Push, retryBase)
				if err != nil {
					// Config validation already parsed the key, so this
					// is not a misconfiguration; fail loudly rather than
					// running with a silent delivery route.
					slog.Error("push: starting deliverer", "err", err)
				} else {
					go p.Run(whCtx)
				}
			}
```

Add `"gitbay.org/gitbay/internal/push"` to the file's imports.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/push/ -v && go build ./... && go vet ./...`
Expected: PASS and clean.

- [ ] **Step 6: Commit**

```bash
git add internal/push/push.go internal/push/push_test.go cmd/gitbayd/main.go
git -c commit.gpgsign=true commit -m "push: drain the queue, started by gitbayd

Two-second ticker in the mailer's shape. A reap drops the device and
its queued rows cascade; a retry honours Retry-After when APNs gave
one. Log lines name the device id, never the token.

Ref #89"
```

---

### Task 7: The control commands

**Files:**
- Modify: `internal/control/notifications.go`
- Modify: `cmd/gitbay/main.go:64-73`
- Test: `internal/control/notifications_test.go`

**Interfaces:**
- Consumes: the store device functions from Task 1.
- Produces: registry entries `notifications device add|list|remove` and `notifications settings push`; `emitNotificationSettings` gains a `push` key.

- [ ] **Step 1: Write the failing test**

Add to `internal/control/notifications_test.go` (create it if absent,
following `internal/control/mr_test.go` for how a `Ctx` is built):

```go
func TestNotificationsDeviceAddReadsStdin(t *testing.T) {
	c := testCtx(t, "alice")
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
	c := testCtx(t, "alice")
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

func TestNotificationsSettingsShowsPush(t *testing.T) {
	c := testCtx(t, "alice")
	var out bytes.Buffer
	c.Stdout, c.JSON = &out, true
	if code := runNotificationsSettingsShow(c, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), `"push":true`) {
		t.Fatalf("no push key: %s", out.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/control/ -run TestNotifications -v`
Expected: FAIL — `runNotificationsDeviceAdd` undefined.

- [ ] **Step 3: Register and implement the commands**

In the `init()` of `internal/control/notifications.go`:

```go
	register(Command{Path: []string{"notifications", "device", "add"},
		Summary: "register an Apple device for push, token on stdin",
		Usage:   "notifications device add [--label <name>] < token",
		// Mandatory: without it control.go swaps in an empty reader and
		// this command stores an empty token without erroring.
		ReadsStdin: true, Run: runNotificationsDeviceAdd})
	register(Command{Path: []string{"notifications", "device", "list"},
		Summary:  "your registered devices",
		Usage:    "notifications device list",
		ReadOnly: true, Run: runNotificationsDeviceList})
	register(Command{Path: []string{"notifications", "device", "remove"},
		Summary: "deregister a device",
		Usage:   "notifications device remove <id>", Run: runNotificationsDeviceRemove})
	register(Command{Path: []string{"notifications", "settings", "push"},
		Summary: "activity on your registered devices as well as the inbox",
		Usage:   "notifications settings push on|off", Run: runNotificationsSettingsPush})
```

And the implementations:

```go
// maxDeviceTokenBytes is well past APNs' 32-byte token rendered as 64 hex
// characters, and stops a stdin that is not a token from becoming a row.
const maxDeviceTokenBytes = 512

func runNotificationsDeviceAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--label"}, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 0 {
		return c.usage()
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, maxDeviceTokenBytes+1))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading stdin: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return c.usageWith("no device token on stdin")
	}
	if len(token) > maxDeviceTokenBytes {
		return c.fail(protocol.ExitUsage, "device token is too long")
	}
	if _, err := c.Store.AddPushDevice(c.User.ID, token, f.Value("--label")); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"status": "registered"}, func(w io.Writer) {
		fmt.Fprintln(w, "device registered")
	})
}

func runNotificationsDeviceList(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	devices, err := c.Store.PushDevices(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type row struct {
		ID    int64  `json:"id"`
		Label string `json:"label"`
		Token string `json:"token"` // truncated; a token is not echoed in full
		Added string `json:"added"`
	}
	rows := make([]row, 0, len(devices))
	for _, d := range devices {
		rows = append(rows, row{ID: d.ID, Label: d.Label,
			Token: shortToken(d.Token), Added: d.CreatedAt})
	}
	return c.emit(rows, func(w io.Writer) {
		for _, r := range rows {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", r.ID, r.Label, r.Token, r.Added)
		}
	})
}

// shortToken renders a device token as its first eight characters. Enough
// to tell two devices apart in a list, not enough to push to one.
func shortToken(t string) string {
	if len(t) <= 8 {
		return t
	}
	return t[:8] + "…"
}

func runNotificationsDeviceRemove(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.usageWith("device id must be a number")
	}
	if err := c.Store.RemovePushDevice(c.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no such device; notifications device list shows yours")
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"status": "removed"}, func(w io.Writer) {
		fmt.Fprintln(w, "device removed")
	})
}

func runNotificationsSettingsPush(c *Ctx, args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return c.usage()
	}
	if err := c.Store.SetPushEnabled(c.User.ID, args[0] == "on"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitNotificationSettings(c)
}
```

Add `"errors"` and `"gitbay.org/gitbay/internal/store"` to the imports if
the file lacks them.

- [ ] **Step 4: Extend `emitNotificationSettings`**

Replace the body of `emitNotificationSettings` so it reads `push` too and
adds it to both outputs:

```go
	push, err := c.Store.PushEnabled(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]bool{"mail": mail, "watch": watch, "push": push}, func(w io.Writer) {
		...
		fmt.Fprintf(w, "mail: %s\nwatch: %s\npush: %s\n", onOff(mail), onOff(watch), onOff(push))
	})
```

- [ ] **Step 5: Add the CLI passthroughs**

In `cmd/gitbay/main.go`, inside the `notifications` group around line 64,
add a `device` subgroup and the settings entry:

```go
			group("device", "Apple devices registered for push",
				pass("add", "register a device, token on stdin: [--label name]",
					passOpts{server: []string{"notifications", "device", "add"}, stdin: true}),
				pass("list", "your registered devices",
					passOpts{server: []string{"notifications", "device", "list"}}),
				pass("remove", "deregister a device: <id>",
					passOpts{server: []string{"notifications", "device", "remove"}}),
			),
```

and beside `mail` and `watch` in the settings group:

```go
				pass("push", "activity on your registered devices: on|off", passOpts{server: []string{"notifications", "settings", "push"}}),
```

Check `passOpts`' real field for a stdin-reading command against how
`snippet create` is registered in the same file — use that name, not
`stdin:` if it differs.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/control/ ./cmd/gitbay/ -v`
Expected: PASS. Four registry tests exercise the new commands without
being edited: `TestStdinCommandsReadStdin` (which fails if `device add`
lacks `ReadsStdin`), `TestReadOnlyCommandsWriteNothing`, the
`cmd/gitbay` coverage test (which fails without the `pass()` entries),
and the usage-literal check.

- [ ] **Step 7: Commit**

```bash
git add internal/control/notifications.go internal/control/notifications_test.go cmd/gitbay/main.go
git -c commit.gpgsign=true commit -m "control: notifications device and settings push

Token on stdin, never argv. device list truncates the token to eight
characters: enough to tell two devices apart, not enough to push to
one. settings show gains a third key.

Ref #89"
```

---

### Task 8: Push as the third route in `notify()`

**Files:**
- Modify: `internal/control/notifications.go:66-85` (`notify`)
- Test: `internal/control/notifications_test.go`

**Interfaces:**
- Consumes: `EnqueuePush` from Task 2.
- Produces: `func pushTitle(n notice) string` and `func pushBody(n notice) string`.

- [ ] **Step 1: Write the failing test**

```go
func TestNotifyQueuesPush(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t) // alice acts, bob watches
	c.Store.AddPushDevice(bob, "tok-b", "iphone")

	notify(c, []int64{bob}, notice{repo: repo, kind: "issue",
		subject: "[alice/app] #1: title",
		action:  "opened issue #1",
		path:    "alice/app/issues/1"})

	due, err := c.Store.DuePush(20)
	if err != nil {
		t.Fatalf("DuePush: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("want one queued push, got %d", len(due))
	}
	// The push body is the inbox row's summary, so the two surfaces
	// cannot disagree about what happened.
	if due[0].Title != "alice/app" {
		t.Fatalf("title = %q", due[0].Title)
	}
	if due[0].Body != "alice opened issue #1" {
		t.Fatalf("body = %q", due[0].Body)
	}
	if due[0].Path != "alice/app/issues/1" {
		t.Fatalf("path = %q", due[0].Path)
	}
}

func TestNotifyQueuesNoPushForTheActor(t *testing.T) {
	c, repo, _ := testRepoWithWatcher(t)
	c.Store.AddPushDevice(c.User.ID, "tok-self", "iphone")

	notify(c, []int64{c.User.ID}, notice{repo: repo, kind: "issue",
		subject: "s", action: "opened issue #1", path: "alice/app/issues/1"})

	// NotifyRecipients already drops the actor; push inherits that and
	// must not find its own way around it.
	if due, _ := c.Store.DuePush(20); len(due) != 0 {
		t.Fatalf("queued a push to the actor")
	}
}
```

Write `testRepoWithWatcher` to return a `*Ctx` acting as alice, a
`store.Repo` she owns, and bob's user id with a watch row on it. Follow
whatever `internal/control`'s existing tests do to build a repo.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/control/ -run TestNotifyQueues -v`
Expected: FAIL — one queued push wanted, none found.

- [ ] **Step 3: Write the implementation**

In `notify()`, inside the existing `for _, id := range recipients` loop,
after `AddNotice` and before the `if !sendMail { continue }`:

```go
		c.Store.EnqueuePush(id, pushTitle(n), pushBody(c.User.Username, n), n.path)
```

Putting it above the `continue` matters — an instance without SMTP still
pushes.

And beside `noticeBody`:

```go
// pushTitle and pushBody are the alert's two lines. The body is built
// from the same two values AddNotice files, so the alert and the inbox
// row cannot disagree about what happened. The title is the repository,
// which also groups a repository's notices in Notification Center.
func pushTitle(n notice) string { return n.repo.Path() }

func pushBody(actor string, n notice) string { return actor + " " + n.action }
```

`notice` has no `actor` field and does not gain one: the actor is
`c.User.Username`, already passed to `AddNotice` on the line above, so
threading it through the struct would be a second copy of the same
value. The call in the loop is therefore:

```go
		c.Store.EnqueuePush(id, pushTitle(n), pushBody(c.User.Username, n), n.path)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/control/ -v`
Expected: PASS, the whole package — `notify` has sixteen call sites and
this changes all of them.

- [ ] **Step 5: Commit**

```bash
git add internal/control/notifications.go internal/control/notifications_test.go
git -c commit.gpgsign=true commit -m "control: push as the third route in notify

Queued in the same loop as the inbox row and the mail, above the SMTP
check so an instance without a relay still pushes. The alert body is
the inbox summary, so the surfaces cannot disagree.

Ref #89"
```

---

### Task 9: `issue assign` files a notice

**Files:**
- Modify: `internal/control/issue.go:423-470`
- Test: `internal/control/issue_test.go`

**Interfaces:**
- Consumes: `notify`, `notice` from Task 8.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

```go
func TestIssueAssignNotifiesTheAssignee(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)

	if code := runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	rows, _ := c.Store.Inbox(bob, false, 20, 0)
	if len(rows) != 1 || rows[0].Summary != "assigned you to #1" {
		t.Fatalf("got %+v", rows)
	}
}

func TestIssueAssignIsSilentForTheActorAndForRemovals(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)

	// Assigning yourself announces nothing: notify drops the actor.
	runIssueAssign(c, []string{repo.Path(), "1", "--add", "alice"})
	if rows, _ := c.Store.Inbox(c.User.ID, false, 20, 0); len(rows) != 0 {
		t.Fatalf("self-assignment notified: %+v", rows)
	}

	// Unassigning files nothing.
	runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"})
	before, _ := c.Store.Inbox(bob, false, 20, 0)
	runIssueAssign(c, []string{repo.Path(), "1", "--remove", "bob"})
	after, _ := c.Store.Inbox(bob, false, 20, 0)
	if len(after) != len(before) {
		t.Fatalf("removal filed a row: %d then %d", len(before), len(after))
	}
}
```

The helper needs an issue #1 on the repo; extend `testRepoWithWatcher`
from Task 8 or add a sibling that also opens one.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/control/ -run TestIssueAssign -v`
Expected: FAIL — the inbox is empty.

- [ ] **Step 3: Write the implementation**

In `runIssueAssign`, collect the ids as the add loop resolves them:

```go
	var added []int64
	for _, name := range adds {
		u, code := resolve(name)
		if code >= 0 {
			return code
		}
		if err := c.Store.SetIssueAssignee(issue.ID, u.ID, true); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		added = append(added, u.ID)
	}
```

and after both loops succeed, before the function's existing return:

```go
	if len(added) > 0 {
		// direct, as a mention is: an assignment is addressed to someone,
		// and widening it to watchers would tell them "assigned you".
		// Removals file nothing, and notify drops the actor, so assigning
		// yourself is silent.
		notify(c, added, notice{repo: repo, kind: "issue", direct: true,
			subject: fmt.Sprintf("[%s] #%d: %s", repo.Path(), issue.Number, issue.Title),
			action:  fmt.Sprintf("assigned you to #%d", issue.Number),
			path:    fmt.Sprintf("%s/issues/%d", repo.Path(), issue.Number)})
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/control/ -run TestIssueAssign -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/issue.go internal/control/issue_test.go
git -c commit.gpgsign=true commit -m "control: issue assign files a notice

The dashboard surfaced assigned work and nothing announced it. Direct,
as a mention is, so watchers are not told they were assigned.

Ref #89"
```

---

### Task 10: The web settings page

**Files:**
- Modify: `internal/httpd/account.go` — `accountPage` around `:65-66`, the page struct around `:76`, and the `accountSubmit` switch at `:246`
- Modify: `internal/web/templates/account.html` — the `#notifications` section at `:132`
- Test: `internal/httpd/` package tests

The page is `/settings`, rendered from `account.html`, with a
`#notifications` section that already carries the mail and watch
toggles. No new template file, so `TestMainWidthClass` is not involved.

**Interfaces:**
- Consumes: the control commands from Task 7.
- Produces: nothing other tasks use.

- [ ] **Step 1: Write the failing test**

Follow the existing `internal/httpd` account-page test for how a page is
rendered and its body captured.

```go
func TestAccountPagePushToggleAndDevices(t *testing.T) {
	// ... render /settings for a user with one registered device whose
	// token is `strings.Repeat("a", 64)`.
	if !strings.Contains(body, `value="notify-push"`) {
		t.Fatal("no push toggle")
	}
	if !strings.Contains(body, "iphone") {
		t.Fatal("the device is not listed")
	}
	// A token is device-identifying and is never printed in full.
	if strings.Contains(body, strings.Repeat("a", 64)) {
		t.Fatal("the page printed a device token in full")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpd/ -run TestAccountPage -v`
Expected: FAIL — no push toggle.

- [ ] **Step 3: Accept the new field**

In `accountSubmit` (`internal/httpd/account.go:246`), the case derives
`pref` by trimming `notify-`, so this is one token:

```go
	case "notify-mail", "notify-watch", "notify-push":
```

- [ ] **Step 4: Render the toggle and the list**

In `accountPage`, beside `mailOn` and `watchOn`:

```go
	pushOn, _ := s.st.PushEnabled(u.ID)
	devices, _ := s.st.PushDevices(u.ID)
```

Add `PushOn bool` and a device slice to the anonymous page struct in the
`s.render` call. Render each device with `prefix8` — the truncation
helper this file already uses for key fingerprints — not the full token.

In `account.html`, after the watch form in the `#notifications` section,
copying the shape of the two forms already there:

```html
<form method="post" action="/settings" class="setform">
  <input type="hidden" name="field" value="notify-push">
  <label for="notify-push">Activity on your registered devices</label>
  <input type="checkbox" id="notify-push" name="push" value="on"{{if .PushOn}} checked{{end}}>
  <button type="submit" class="btn">Save</button>
</form>
<p class="meta">Notification text is sent in full, including for private repositories, so a repository name and item number reach Apple and appear on a lock screen.</p>
```

Then the device list, each row posting `field=device-remove` with the
id, dispatched through `s.runControl` to
`[]string{"notifications", "device", "remove", id}` as a new case in the
same switch.

There is no add-a-device form: a browser cannot produce an APNs token.
That is the Parity page's "CLI only, for now", not a refusal.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/httpd/ ./internal/web/ -v`
Expected: PASS. Run `./internal/web/` too — the template registry tests
live there and a malformed template fails at render, not at build.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/ internal/web/
git -c commit.gpgsign=true commit -m "web: push toggle and device list on notification settings

No add-a-device form: a browser cannot produce an APNs token.

Ref #89"
```

---

### Task 11: End-to-end

**Files:**
- Create: `e2e/push_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Write the test**

`e2e/push_test.go`, following `e2e/bookmarks_test.go` for how an instance
and accounts are set up:

```go
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A push reaches a registered device with the same words the inbox row
// carries, and a token Apple has retired takes its device with it.
func TestPush(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	var gone bool

	apns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(raw, &payload)
		mu.Lock()
		defer mu.Unlock()
		if gone {
			w.WriteHeader(410)
			io.WriteString(w, `{"reason":"Unregistered"}`)
			return
		}
		got = append(got, payload)
		w.WriteHeader(200)
	}))
	defer apns.Close()

	keyPath := writeTestAPNSKey(t)
	t.Setenv("GITBAY_APNS_HOST", strings.TrimPrefix(apns.URL, "http://"))
	inst := startInstanceWith(t, `[push]
enabled = true
key_file = "`+keyPath+`"
key_id = "KEYID"
team_id = "TEAMID"
topic = "org.gitbay.gitbay"
environment = "production"
`)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Bob watches alice's repository and registers a device.
	if out, errOut, code := inst.ssh(t, bobKey, "", "repo", "watch", "alice/app"); code != 0 {
		t.Fatalf("watch: %s%s", out, errOut)
	}
	if out, errOut, code := inst.ssh(t, bobKey, "DEVTOKEN\n", "notifications", "device", "add", "--label", "iphone"); code != 0 {
		t.Fatalf("device add: %s%s", out, errOut)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "notifications", "device", "list", "--json"); !strings.Contains(out, `"label":"iphone"`) {
		t.Fatalf("device not listed:\n%s", out)
	} else if strings.Contains(out, "DEVTOKEN") {
		t.Fatalf("device list printed the token in full:\n%s", out)
	}

	// Alice opens an issue. Bob hears about it.
	if out, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "a bug", "--body", "x"); code != 0 {
		t.Fatalf("issue create: %s%s", out, errOut)
	}

	waitFor(t, 20*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}, "no push arrived")

	mu.Lock()
	aps := got[0]["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	mu.Unlock()
	if alert["title"] != "alice/app" {
		t.Fatalf("title = %v", alert["title"])
	}
	// The same words the inbox row carries.
	if body, _ := alert["body"].(string); !strings.Contains(body, "opened issue #1") {
		t.Fatalf("body = %q", body)
	}
	if got[0]["path"] != "alice/app/issues/1" {
		t.Fatalf("path = %v", got[0]["path"])
	}

	// Apple retires the token. The next push reaps the device.
	mu.Lock()
	gone = true
	mu.Unlock()
	if out, errOut, code := inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "1", "--body", "ping"); code != 0 {
		t.Fatalf("issue comment: %s%s", out, errOut)
	}
	waitFor(t, 20*time.Second, func() bool {
		out, _, _ := inst.ssh(t, bobKey, "", "notifications", "device", "list", "--json")
		return !strings.Contains(out, "iphone")
	}, "the device survived a 410")

	// The inbox is untouched by any of it: push is a side channel.
	if out, _, _ := inst.ssh(t, bobKey, "", "notifications", "list", "--json"); !strings.Contains(out, "opened issue #1") {
		t.Fatalf("inbox missing the notice:\n%s", out)
	}
}
```

Write `writeTestAPNSKey` (a P-256 PKCS#8 key in a `t.TempDir()`, as in
Task 3) and reuse the e2e suite's existing polling helper rather than
writing `waitFor` if one exists — check `e2e/` for it first.

Note the `ssh` helper's second argument is stdin; that is how `DEVTOKEN`
reaches `device add`.

- [ ] **Step 2: Run it**

Run: `go test ./e2e/ -run TestPush -v`
Expected: PASS. This is the one e2e test to run locally; the rest of the
suite belongs to CI on bay1.

- [ ] **Step 3: Commit**

```bash
git add e2e/push_test.go
git -c commit.gpgsign=true commit -m "e2e: push delivery and device reaping

A fake APNs over HTTP/1.1; the real transport is h2 by ALPN, which is
stdlib behaviour and not ours to test.

Ref #89"
```

---

### Task 12: Documentation

**Files:**
- Modify: `.gitbay/wiki/Parity.md`
- Modify: `.gitbay/wiki/Admin.md`
- Modify: `.gitbay/wiki/Users.md`
- Modify: `CHANGELOG.org`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Parity**

Add a row per new command — `notifications device add`, `device list`,
`device remove`, `settings push` — with its SSH/CLI/web/API columns.
`device add` is CLI only for now on the web column, because a browser
cannot produce an APNs token; write it as "CLI only, for now", which is
the page's current wording, not "no".

- [ ] **Step 2: Admin**

Document the `[push]` section: every key, how to obtain a `.p8` from the
developer portal, where the file goes (`/etc/gitbay/apns.p8`, mode 0600,
owned by the account gitbayd runs as), and the constraint that an APNs
key belongs to a bundle ID — a self-hoster pushes to their own build
under their own `topic`, not to the App Store app.

- [ ] **Step 3: Users**

Document `notifications settings push on|off` and what a device row is:
registered by the app, listed and removable from the CLI and the web,
and dropped automatically when Apple says the token is dead.

State plainly that notification text is sent in full, private
repositories included, so a repository name and item number reach Apple
and appear on a lock screen.

- [ ] **Step 4: CHANGELOG**

Add the feature under the unreleased heading in `CHANGELOG.org`, in the
style of the entries already there.

- [ ] **Step 5: Commit**

```bash
git add .gitbay/wiki/ CHANGELOG.org
git -c commit.gpgsign=true commit -m "docs: push notifications

Closes #89"
```

---

### Task 13: Open the merge request

- [ ] **Step 1: Verify the branch**

Run: `go build ./... && go vet ./... && go test ./internal/... ./cmd/...`
Expected: all PASS. Do not claim the branch is green without this output
in front of you.

- [ ] **Step 2: Push and open the MR**

```bash
git push -u origin ios-push
```

```bash
gitbay mr create --source ios-push --target main --title "iOS push notifications (server)"
```

Body via `--file -` from a file, not a heredoc. It states what landed and
references `Closes #89`. No attribution to any assistant or model.

- [ ] **Step 3: Let CI run**

The e2e suite runs on bay1. Watch it with `gitbay build list` and
`gitbay build log <n>`. Do not poll with several ssh calls per tick —
the auth limiter reads a burst as an attack.

- [ ] **Step 4: Merge**

Only with the full suite green:

```bash
gitbay mr merge <n> --strategy ff
```

Signed commits are required, so `squash` and `merge` are refused — both
would mint an unsigned commit. If the merge reports the branch is
behind, rebase onto `main`, re-push, merge again. Then delete the branch
locally and remotely.

**Do not deploy.** `[push]` stays `enabled = false` on bay1 until the app
is submitted; there is nothing to deliver to until a device registers.

---

## Notes for whoever executes this

- **Tasks 1-9 are the working feature.** Task 10 (web) and Task 12 (docs)
  can be reordered or split into a follow-up MR if the branch is getting
  long, but Task 11's e2e should land with the code it tests.
- **The classifier may refuse some of this.** Editing files under
  `internal/policy/` or anything that reads as relaxing an access-control
  flag has been refused before in auto mode. Nothing in this plan should
  trip it, but if a refusal happens, retry as a single-file edit with no
  chained build rather than treating it as a puzzle.
- **The `krz/gitbay-ios` half is a separate plan** on that repository,
  written once this has shipped. The spec's "The app" section is the
  contract it has to meet — payload keys `aps.alert.title`,
  `aps.alert.body`, `aps.thread-id` and top-level `path`, and the
  `notifications device add|remove` calls.
