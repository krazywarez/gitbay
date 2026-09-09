# Runners attached to repositories: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `gitbay-runner` anyone installs, pairs with their repositories on any instance, and runs as a service; the server hands a runner key only the builds of repositories it is attached to.

**Architecture:** One new table (`runner_repos`) maps an SSH key to repositories; `runner next` claims only from a key's attachments and skips untrusted builds unless asked; `repo runner add|list|remove` manage attachments and render on the settings page. The runner gains `init`, a config file, its own identity, and `-untrusted`.

**Tech Stack:** Go, SQLite via hand-written SQL, `github.com/BurntSushi/toml` (already a dependency), Go templates, e2e tests against real ssh/git.

**Spec:** `docs/specs/2026-09-08-user-runners-design.md`

## Global Constraints

- Commit messages: `<area>, <area>: <what>` on the first line, body with `Ref #184`. No attribution trailers of any kind (top rule of `~/CLAUDE.md`).
- Never push to `main`. Work on branch `user-runners`; MR at the end.
- Locally: `go build ./... && go vet ./...`, the unit tests of the touched packages, and at most the one e2e test being written. The full suite runs in CI on bay1.
- Every control command parses argv through `parseFlags` (`internal/control/flags.go`). A command that reads stdin sets `ReadsStdin: true`. A read command sets `ReadOnly: true`.
- Every new control command needs a `pass()` entry in `cmd/gitbay/main.go`; a coverage test fails otherwise.
- Secrets never in argv, never logged. A public key is not a secret.
- Templates: `str`/`field` are nil-safe helpers; the whole stylesheet is `internal/web/static/style.css`.
- Migrations: next number is `0050`, both `.up.sql` and `.down.sql`. Foreign keys are on.
- Comments and docs in plain English, no hype. Wiki is `.gitbay/wiki/*.org`.

---

### Task 1: Store: ClaimBuild skips untrusted builds unless asked

**Files:**
- Modify: `internal/store/builds.go:80-118` (`ClaimBuild`)
- Modify: `internal/store/builds_test.go` (every `ClaimBuild(` call gains `, false`; one new test)
- Modify: `internal/store/queues_test.go:27` (`ClaimBuild(nil, false)`)

**Interfaces:**
- Produces: `Store.ClaimBuild(repoIDs []int64, untrusted bool) (Build, bool, error)`. With `untrusted` false only rows with `trusted = 1` are candidates.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/builds_test.go`:

```go
// A merge request head from a fork is untrusted. A claim skips it unless
// the runner asked for untrusted builds, so a runner on someone's laptop
// never executes a stranger's branch by default.
func TestClaimBuildSkipsUntrustedUnlessAsked(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	// Queued first, so an unfiltered claim would take it.
	forkBuild, err := s.CreateBuild(repo, "unit", "abc123", "refs/merge-requests/1/head", `["true"]`, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.CreateBuild(repo, "unit", "def456", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.ClaimBuild(nil, false)
	if err != nil || !ok || b.Number != own {
		t.Fatalf("trusted-only claim: err=%v ok=%v number=%d, want %d", err, ok, b.Number, own)
	}
	if _, ok, _ := s.ClaimBuild(nil, false); ok {
		t.Fatal("trusted-only claim took the fork build")
	}
	b, ok, err = s.ClaimBuild(nil, true)
	if err != nil || !ok || b.Number != forkBuild {
		t.Fatalf("untrusted claim: err=%v ok=%v number=%d, want %d", err, ok, b.Number, forkBuild)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/store -run TestClaimBuildSkipsUntrusted 2>&1 | head -5`
Expected: compile error, too many arguments to `ClaimBuild`.

- [ ] **Step 3: Change `ClaimBuild`**

Replace the signature, doc comment and query construction in `internal/store/builds.go`:

```go
// ClaimBuild atomically hands the oldest pending build to a runner and
// marks it running. A non-empty repoIDs restricts the claim to those
// repositories. Untrusted builds — merge request heads from another
// repository — are skipped unless untrusted is set: they run a stranger's
// code, which only a runner that isolates should take.
func (s *Store) ClaimBuild(repoIDs []int64, untrusted bool) (Build, bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return Build{}, false, err
	}
	defer tx.Rollback()
	query := "SELECT id FROM builds WHERE status = 'pending'"
	args := []any{}
	if !untrusted {
		query += " AND trusted = 1"
	}
	if len(repoIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(repoIDs)), ",")
		query += " AND repo_id IN (" + marks + ")"
		for _, id := range repoIDs {
			args = append(args, id)
		}
	}
	query += " ORDER BY id LIMIT 1"
	var id int64
	err = tx.QueryRow(query, args...).Scan(&id)
```

The rest of the function is unchanged.

- [ ] **Step 4: Update existing callers in store tests**

In `internal/store/builds_test.go` and `internal/store/queues_test.go`, every `s.ClaimBuild(x)` becomes `s.ClaimBuild(x, false)`:

```bash
sed -i '' -E 's/ClaimBuild\((nil|\[\]int64\{[a-zA-Z]+\})\)/ClaimBuild(\1, false)/g' internal/store/builds_test.go internal/store/queues_test.go
grep -n "ClaimBuild(" internal/store/*_test.go
```

Every hit must now show two arguments.

- [ ] **Step 5: Run the store tests**

Run: `go test ./internal/store 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/builds.go internal/store/builds_test.go internal/store/queues_test.go
git commit -m "store: ClaimBuild skips untrusted builds unless asked

Ref #184"
```

---

### Task 2: Store: migration 0050 and runner attachments

**Files:**
- Create: `internal/store/migrations/0050_runner_repos.up.sql`
- Create: `internal/store/migrations/0050_runner_repos.down.sql`
- Modify: `internal/store/runners.go` (whole file)
- Create: `internal/store/runners_test.go`

**Interfaces:**
- Consumes: `Store.AddSSHKey(userID int64, fingerprint, algo string, blob []byte, scope string) error`, `Store.SSHKeyByFingerprint(fp) (SSHKey, error)`, `Store.CreateUser(name string, admin bool) (int64, error)`, `Store.CreateRepo(kind string, ownerID int64, name, visibility string) (int64, error)`, `Store.CreateBuild(repoID int64, job, sha, ref, steps, image, tree string, trusted bool) (int64, error)`.
- Produces:
  - `type RepoRunner struct { Fingerprint, Algo, Username, AddedAt, LastSeen, BuildRepo string; BuildNumber int64; BuildJob, StartedAt string }`
  - `Runner` gains `Fingerprint string` and `KeyID int64`.
  - `Store.AttachRunner(keyID, repoID int64) error` (idempotent)
  - `Store.DetachRunner(repoID int64, fingerprint string) error` (`ErrNotFound` when not attached)
  - `Store.RunnerRepoIDs(keyID int64) ([]int64, error)`
  - `Store.RunnerRepoPaths(keyID int64) ([]string, error)` (owner/name, sorted)
  - `Store.RunnerAttached(keyID, repoID int64) (bool, error)`
  - `Store.ListRepoRunners(repoID int64) ([]RepoRunner, error)`
  - `Store.TouchRunner(keyID, userID int64, scope string, buildID int64) error`
  - `Store.RunnerDone(keyID int64) error`
  - `Store.ListRunners() ([]Runner, error)` unchanged signature.

- [ ] **Step 1: Write the migration**

`internal/store/migrations/0050_runner_repos.up.sql`:

```sql
-- A runner key is attached to the repositories it may claim builds for
-- (#184). runner_seen is rekeyed by key so two runners on one account
-- are two rows; what it held were heartbeats, so the rows are dropped.
CREATE TABLE runner_repos (
    key_id   INTEGER NOT NULL REFERENCES ssh_keys(id) ON DELETE CASCADE,
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (key_id, repo_id)
);
CREATE INDEX runner_repos_repo ON runner_repos(repo_id);

DROP TABLE runner_seen;
CREATE TABLE runner_seen (
    key_id    INTEGER PRIMARY KEY REFERENCES ssh_keys(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_seen TEXT NOT NULL,
    scope     TEXT NOT NULL DEFAULT '',
    build_id  INTEGER REFERENCES builds(id) ON DELETE SET NULL
);
```

`internal/store/migrations/0050_runner_repos.down.sql`:

```sql
DROP TABLE runner_repos;
DROP TABLE runner_seen;
CREATE TABLE runner_seen (
    user_id   INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_seen TEXT NOT NULL,
    scope     TEXT NOT NULL DEFAULT '',
    build_id  INTEGER REFERENCES builds(id) ON DELETE SET NULL
);
```

- [ ] **Step 2: Write the failing store tests**

`internal/store/runners_test.go`:

```go
package store

import (
	"errors"
	"testing"
)

// runnerFixture is one user with a runner key and two repositories.
func runnerFixture(t *testing.T) (s *Store, uid, keyID, repoA, repoB int64) {
	t.Helper()
	s = open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKey(uid, "SHA256:runnerkey", "ssh-ed25519", []byte("blob"), "runner"); err != nil {
		t.Fatal(err)
	}
	k, err := s.SSHKeyByFingerprint("SHA256:runnerkey")
	if err != nil {
		t.Fatal(err)
	}
	repoA, err = s.CreateRepo("user", uid, "a", "public")
	if err != nil {
		t.Fatal(err)
	}
	repoB, err = s.CreateRepo("user", uid, "b", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, uid, k.ID, repoA, repoB
}

// Attaching twice is one row; detaching what is not attached is not found.
func TestAttachRunnerIdempotentAndDetach(t *testing.T) {
	s, _, keyID, repoA, repoB := runnerFixture(t)
	for range 2 {
		if err := s.AttachRunner(keyID, repoA); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.RunnerRepoIDs(keyID)
	if err != nil || len(ids) != 1 || ids[0] != repoA {
		t.Fatalf("attached repos %v err=%v, want [%d]", ids, err, repoA)
	}
	if ok, _ := s.RunnerAttached(keyID, repoB); ok {
		t.Fatal("attached to a repo it was never attached to")
	}
	if err := s.DetachRunner(repoB, "SHA256:runnerkey"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("detach of an unattached repo: %v, want ErrNotFound", err)
	}
	if err := s.DetachRunner(repoA, "SHA256:runnerkey"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.RunnerAttached(keyID, repoA); ok {
		t.Fatal("still attached after detach")
	}
}

// Removing the key or the repository removes the attachment with it.
func TestRunnerAttachmentCascades(t *testing.T) {
	s, uid, keyID, repoA, repoB := runnerFixture(t)
	if err := s.AttachRunner(keyID, repoA); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachRunner(keyID, repoB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM repos WHERE id = ?", repoB); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.RunnerRepoIDs(keyID); len(ids) != 1 {
		t.Fatalf("after repo delete: %v, want one attachment", ids)
	}
	if err := s.RemoveSSHKey(uid, "SHA256:runnerkey"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM runner_repos").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after key delete: %d rows err=%v, want 0", n, err)
	}
}

// The heartbeat is per key: two keys on one account are two rows, and a
// repository's runner list shows each key's last poll and the build it holds.
func TestRunnerSeenPerKeyAndRepoList(t *testing.T) {
	s, uid, keyID, repoA, _ := runnerFixture(t)
	if err := s.AddSSHKey(uid, "SHA256:second", "ssh-ed25519", []byte("blob2"), "runner"); err != nil {
		t.Fatal(err)
	}
	k2, _ := s.SSHKeyByFingerprint("SHA256:second")
	for _, id := range []int64{keyID, k2.ID} {
		if err := s.AttachRunner(id, repoA); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBuild(repoA, "unit", "abc123", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.ClaimBuild(nil, false)
	if err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}
	if err := s.TouchRunner(keyID, uid, "", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchRunner(k2.ID, uid, "", 0); err != nil {
		t.Fatal(err)
	}
	runners, err := s.ListRunners()
	if err != nil || len(runners) != 2 {
		t.Fatalf("ListRunners: %v err=%v, want two rows", runners, err)
	}
	list, err := s.ListRepoRunners(repoA)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRepoRunners: %v err=%v, want two rows", list, err)
	}
	var held, idle int
	for _, r := range list {
		if r.Username != "alice" || r.LastSeen == "" || r.AddedAt == "" {
			t.Fatalf("row %+v lacks username, last_seen or added_at", r)
		}
		if r.BuildNumber == b.Number && r.BuildJob == "unit" && r.BuildRepo == "alice/a" {
			held++
		} else if r.BuildNumber == 0 {
			idle++
		}
	}
	if held != 1 || idle != 1 {
		t.Fatalf("held=%d idle=%d, want 1 and 1: %+v", held, idle, list)
	}
	if err := s.RunnerDone(keyID); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListRepoRunners(repoA)
	for _, r := range list {
		if r.BuildNumber != 0 {
			t.Fatalf("build still held after RunnerDone: %+v", r)
		}
	}
	paths, err := s.RunnerRepoPaths(keyID)
	if err != nil || len(paths) != 1 || paths[0] != "alice/a" {
		t.Fatalf("RunnerRepoPaths: %v err=%v", paths, err)
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./internal/store -run 'TestAttachRunner|TestRunnerAttachment|TestRunnerSeen' 2>&1 | head -20`
Expected: compile errors, `s.AttachRunner undefined` and friends.

- [ ] **Step 4: Replace `internal/store/runners.go`**

```go
package store

import "sort"

// Runner is one runner key as the instance admin sees it.
type Runner struct {
	Username    string `json:"username"`
	Fingerprint string `json:"fingerprint"`
	KeyID       int64  `json:"-"`
	LastSeen    string `json:"last_seen"`
	// Scope is what the runner asked for: comma-joined owner/name, ""
	// for any. admin runners replaces it with the attachments for a
	// runner key.
	Scope string `json:"scope,omitempty"`
	// The build it holds, if any.
	BuildRepo   string `json:"build_repo,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildJob    string `json:"build_job,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// RepoRunner is one key attached to a repository, as repo runner list
// shows it.
type RepoRunner struct {
	Fingerprint string `json:"fingerprint"`
	Algo        string `json:"algo"`
	Username    string `json:"username"`
	AddedAt     string `json:"added_at"`
	LastSeen    string `json:"last_seen,omitempty"`
	BuildRepo   string `json:"build_repo,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildJob    string `json:"build_job,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// AttachRunner lets a key claim a repository's builds. Attaching twice is
// one row.
func (s *Store) AttachRunner(keyID, repoID int64) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO runner_repos (key_id, repo_id) VALUES (?, ?)", keyID, repoID)
	return err
}

// DetachRunner removes one attachment by fingerprint. The key itself stays.
func (s *Store) DetachRunner(repoID int64, fingerprint string) error {
	res, err := s.DB.Exec(`DELETE FROM runner_repos WHERE repo_id = ?
		AND key_id = (SELECT id FROM ssh_keys WHERE fingerprint = ?)`, repoID, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunnerRepoIDs is every repository a key is attached to.
func (s *Store) RunnerRepoIDs(keyID int64) ([]int64, error) {
	rows, err := s.DB.Query("SELECT repo_id FROM runner_repos WHERE key_id = ? ORDER BY repo_id", keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RunnerRepoPaths is RunnerRepoIDs as owner/name, sorted.
func (s *Store) RunnerRepoPaths(keyID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT COALESCE(u.username, o.name) || '/' || r.name
		FROM runner_repos rr JOIN repos r ON r.id = rr.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o ON r.owner_kind = 'org' AND o.id = r.owner_id
		WHERE rr.key_id = ?`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, rows.Err()
}

// RunnerAttached reports whether a key may claim a repository's builds.
func (s *Store) RunnerAttached(keyID, repoID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM runner_repos WHERE key_id = ? AND repo_id = ?", keyID, repoID).Scan(&n)
	return n > 0, err
}

// ListRepoRunners is every key attached to a repository with its last
// poll and the build it holds, oldest attachment first.
func (s *Store) ListRepoRunners(repoID int64) ([]RepoRunner, error) {
	rows, err := s.DB.Query(`SELECT k.fingerprint, k.algo, u.username, rr.added_at,
		COALESCE(rs.last_seen, ''),
		COALESCE(COALESCE(bu.username, bo.name) || '/' || br.name, ''),
		COALESCE(b.number, 0), COALESCE(b.job, ''), COALESCE(b.started_at, '')
		FROM runner_repos rr
		JOIN ssh_keys k ON k.id = rr.key_id
		JOIN users u ON u.id = k.user_id
		LEFT JOIN runner_seen rs ON rs.key_id = rr.key_id
		LEFT JOIN builds b ON b.id = rs.build_id AND b.status = 'running'
		LEFT JOIN repos br ON br.id = b.repo_id
		LEFT JOIN users bu ON br.owner_kind = 'user' AND bu.id = br.owner_id
		LEFT JOIN orgs bo ON br.owner_kind = 'org' AND bo.id = br.owner_id
		WHERE rr.repo_id = ? ORDER BY rr.added_at, k.id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoRunner
	for rows.Next() {
		var r RepoRunner
		if err := rows.Scan(&r.Fingerprint, &r.Algo, &r.Username, &r.AddedAt, &r.LastSeen,
			&r.BuildRepo, &r.BuildNumber, &r.BuildJob, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TouchRunner records a poll by one key: the time, the scope the runner
// asked for, and the build it just claimed (0 for none).
func (s *Store) TouchRunner(keyID, userID int64, scope string, buildID int64) error {
	_, err := s.DB.Exec(`INSERT INTO runner_seen (key_id, user_id, last_seen, scope, build_id)
		VALUES (?1, ?2, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?3, NULLIF(?4, 0))
		ON CONFLICT (key_id) DO UPDATE SET
			last_seen = excluded.last_seen, scope = excluded.scope,
			build_id = COALESCE(excluded.build_id, runner_seen.build_id)`,
		keyID, userID, scope, buildID)
	return err
}

// RunnerDone records that the key reported and holds nothing now.
func (s *Store) RunnerDone(keyID int64) error {
	_, err := s.DB.Exec(`UPDATE runner_seen SET last_seen = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		build_id = NULL WHERE key_id = ?`, keyID)
	return err
}

// ListRunners lists every key that has ever polled as a runner, most
// recently seen first.
func (s *Store) ListRunners() ([]Runner, error) {
	rows, err := s.DB.Query(`SELECT u.username, k.fingerprint, k.id, r.last_seen, r.scope,
		COALESCE(COALESCE(bu.username, bo.name) || '/' || br.name, ''),
		COALESCE(b.number, 0), COALESCE(b.job, ''), COALESCE(b.started_at, '')
		FROM runner_seen r JOIN users u ON u.id = r.user_id
		JOIN ssh_keys k ON k.id = r.key_id
		LEFT JOIN builds b ON b.id = r.build_id AND b.status = 'running'
		LEFT JOIN repos br ON br.id = b.repo_id
		LEFT JOIN users bu ON br.owner_kind = 'user' AND bu.id = br.owner_id
		LEFT JOIN orgs bo ON br.owner_kind = 'org' AND bo.id = br.owner_id
		ORDER BY r.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runner
	for rows.Next() {
		var r Runner
		if err := rows.Scan(&r.Username, &r.Fingerprint, &r.KeyID, &r.LastSeen, &r.Scope,
			&r.BuildRepo, &r.BuildNumber, &r.BuildJob, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run the store tests**

Run: `go test ./internal/store 2>&1 | tail -5`
Expected: PASS. Callers in `internal/control` do not compile yet; that is Task 3. `go build ./internal/store` must pass here.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0050_runner_repos.up.sql internal/store/migrations/0050_runner_repos.down.sql internal/store/runners.go internal/store/runners_test.go
git commit -m "store: runner keys attach to repositories, heartbeat per key

Migration 0050 adds runner_repos and rekeys runner_seen by ssh key.

Ref #184"
```

---

### Task 3: Control: the claim rule, per-key heartbeat, and admin runners

**Files:**
- Modify: `internal/control/build.go` (`requireRunner`, `runRunnerNext`, `runRunnerLog`, `runRunnerDone`, the `runner next` registration)
- Modify: `internal/control/admin.go:444-475` (`runAdminRunners`)
- Modify: `internal/control/runnernext_test.go:17-30` (`runnerCtx`)
- Create: `internal/control/runnerattach_test.go`
- Modify: `e2e/reap_test.go:112-115` (the admin runners row gains a fingerprint column)
- Modify: `e2e/mrbuilds_test.go:95`, `e2e/build_cancel_test.go` (claims of a fork head pass `--untrusted`)

**Interfaces:**
- Consumes: the store functions from Tasks 1 and 2; `Ctx.Source` is the SSH key fingerprint for SSH sessions (`internal/sshd/sshd.go:338`).
- Produces:
  - `runnerSession(c *Ctx) (store.SSHKey, int)`: the key behind the session, or an exit code.
  - `runnerMayBuild(c *Ctx, key store.SSHKey, repoID int64) (bool, error)`: admin, or attached.
  - `runner next [--untrusted] [<owner/name>...]`.
  - `admin runners` JSON rows carry `fingerprint`; the text row is `username<TAB>fingerprint<TAB>last_seen<TAB>scope<TAB>held`.

- [ ] **Step 1: Write the failing control tests**

`internal/control/runnerattach_test.go`:

```go
package control

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// attachFixture: alice (not admin) owns alice/app with a build queued;
// mallory (not admin) owns mallory/evil with an older build queued. Each
// has a runner-scoped key. The Ctx polls as the given user with the given
// key, which is what the SSH listener produces.
type attachFixture struct {
	st                   *store.Store
	alice, mallory       int64
	aliceKey, malloryKey store.SSHKey
	app, evil            store.Repo
	appBuild, evilBuild  int64
}

func newAttachFixture(t *testing.T) attachFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f attachFixture
	f.st = st
	mk := func(name, fp string) (int64, store.SSHKey, store.Repo, string) {
		uid, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddSSHKey(uid, fp, "ssh-ed25519", []byte(fp), "runner"); err != nil {
			t.Fatal(err)
		}
		k, _ := st.SSHKeyByFingerprint(fp)
		repoName := map[string]string{"alice": "app", "mallory": "evil"}[name]
		rid, err := st.CreateRepo("user", uid, repoName, "public")
		if err != nil {
			t.Fatal(err)
		}
		repo, _ := st.RepoByID(rid)
		return uid, k, repo, repoName
	}
	f.mallory, f.malloryKey, f.evil, _ = mk("mallory", "SHA256:mallory")
	f.alice, f.aliceKey, f.app, _ = mk("alice", "SHA256:alice")
	// mallory's build is older, so an unrestricted claim would take it.
	f.evilBuild, err = st.CreateBuild(f.evil.ID, "unit", "aaa111", "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	f.appBuild, err = st.CreateBuild(f.app.ID, "unit", "bbb222", "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f attachFixture) ctx(uid int64, key store.SSHKey, admin bool) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	name := "alice"
	if uid == f.mallory {
		name = "mallory"
	}
	return &Ctx{
		User:   store.User{ID: uid, Username: name, IsAdmin: admin},
		Scope:  key.Scope,
		Source: key.Fingerprint,
		Store:  f.st,
		Cfg:    config.Config{Server: config.Server{Root: "/nonexistent", SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
	}, &out
}

// A runner key with no attachment claims nothing, whatever is queued.
func TestRunnerNextUnattachedClaimsNothing(t *testing.T) {
	f := newAttachFixture(t)
	c, out := f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild)
	if b.Status != "pending" {
		t.Fatalf("unattached key claimed a build: %s", b.Status)
	}
}

// An attached key claims its repository's build and not the older one
// queued elsewhere; naming a repository outside the attachments is refused.
func TestRunnerNextAttachedClaimsOwnRepoOnly(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, nil); code != protocol.ExitOK || !strings.Contains(out.String(), "alice/app") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild); b.Status != "pending" {
		t.Fatalf("mallory's build was touched: %s", b.Status)
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, []string{"mallory/evil"}); code != protocol.ExitDenied {
		t.Fatalf("naming an unattached repo: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
}

// The heartbeat is recorded against the key, and admin runners shows it
// with its fingerprint and attachments.
func TestAdminRunnersShowsKeyAndAttachments(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil)
	admin, out := f.ctx(f.alice, f.aliceKey, true)
	admin.Scope = "full"
	if code := runAdminRunners(admin, nil); code != protocol.ExitOK {
		t.Fatalf("admin runners: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "alice\tSHA256:alice\t") || !strings.Contains(out.String(), "\talice/app\t") {
		t.Fatalf("row lacks fingerprint or attachments:\n%s", out.String())
	}
}

// Untrusted builds are skipped unless the runner asks.
func TestRunnerNextUntrustedFlag(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.aliceKey.ID, f.app.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil) // takes the trusted build
	fork, err := f.st.CreateBuild(f.app.ID, "unit", "ccc333", "refs/merge-requests/1/head", "[]", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice, f.aliceKey, false)
	runRunnerNext(c, nil)
	if !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("fork head claimed without --untrusted: %s", out.String())
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerNext(c, []string{"--untrusted"}); code != protocol.ExitOK || !strings.Contains(out.String(), "alice/app") {
		t.Fatalf("--untrusted did not claim the fork head: exit %d %s", code, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.app.ID, fork); b.Status != "running" {
		t.Fatalf("fork build is %s, want running", b.Status)
	}
}

// runner done and runner log on a build whose repository is not attached
// to the key are refused.
func TestRunnerDoneRefusedForUnattachedBuild(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.st.AttachRunner(f.malloryKey.ID, f.evil.ID); err != nil {
		t.Fatal(err)
	}
	c, _ := f.ctx(f.mallory, f.malloryKey, false)
	runRunnerNext(c, nil) // mallory holds her own build
	evil, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild)
	c, out := f.ctx(f.alice, f.aliceKey, false)
	id := strconv.FormatInt(evil.ID, 10)
	if code := runRunnerDone(c, []string{id, "success"}); code != protocol.ExitDenied {
		t.Fatalf("done on an unattached build: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
	c, out = f.ctx(f.alice, f.aliceKey, false)
	if code := runRunnerLog(c, []string{id}); code != protocol.ExitDenied {
		t.Fatalf("log on an unattached build: exit %d, want %d: %s", code, protocol.ExitDenied, out.String())
	}
	if b, _ := f.st.BuildByNumber(f.evil.ID, f.evilBuild); b.Status != "running" {
		t.Fatalf("build was finished by a foreign key: %s", b.Status)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/control -run 'TestRunnerNext(Unattached|Attached|Untrusted)|TestAdminRunnersShows|TestRunnerDoneRefused' 2>&1 | head`
Expected: compile errors from `ClaimBuild`, `TouchRunner`, `RunnerDone` signature changes in `build.go`.

- [ ] **Step 3: Rewrite the runner protocol in `internal/control/build.go`**

Change the registration:

```go
	register(Command{Path: []string{"runner", "next"},
		Summary: "claim the oldest pending build this key may run (runner protocol)",
		Usage:   "runner next [--untrusted] [<owner/name>...]", SSHOnly: true, Run: runRunnerNext})
```

Replace `requireRunner` with two helpers:

```go
// runnerSession resolves the key behind a runner-protocol session. The
// runner commands are SSHOnly, so Source is the key's fingerprint. An
// admin key is accepted so an operator can rotate at their own pace; a
// runner host should hold a key added with --scope runner.
func runnerSession(c *Ctx) (store.SSHKey, int) {
	if c.Scope != "runner" && !c.User.IsAdmin {
		return store.SSHKey{}, c.fail(protocol.ExitDenied, "runner commands need a key added with --scope runner")
	}
	key, err := c.Store.SSHKeyByFingerprint(c.Source)
	if err != nil {
		return store.SSHKey{}, c.fail(protocol.ExitDenied, "runner commands need an SSH key session")
	}
	return key, -1
}

// runnerMayBuild reports whether a runner session may act on a
// repository's builds: an admin user may on any, a runner key on the
// repositories it is attached to (#184).
func runnerMayBuild(c *Ctx, key store.SSHKey, repoID int64) (bool, error) {
	if c.User.IsAdmin {
		return true, nil
	}
	return c.Store.RunnerAttached(key.ID, repoID)
}
```

Rewrite the head of `runRunnerNext` down to the claim loop:

```go
func runRunnerNext(c *Ctx, args []string) int {
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	f, err := parseFlags(args, flagSpec{Bools: []string{"--untrusted"}, MaxPos: -1,
		Usage: "runner next [--untrusted] [<owner/name>...]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	// The candidate set. An admin key claims from any repository, narrowed
	// by the names given. A runner key claims from the repositories it is
	// attached to; a name outside them is refused, not ignored, so a
	// misconfigured runner says so instead of idling.
	var repoIDs []int64
	for _, arg := range f.Pos {
		repo, code := resolveRepo(c, arg, policy.CanRead)
		if code >= 0 {
			return code
		}
		ok, err := runnerMayBuild(c, key, repo.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !ok {
			return c.fail(protocol.ExitDenied, "this key is not attached to %s", repo.Path())
		}
		repoIDs = append(repoIDs, repo.ID)
	}
	if !c.User.IsAdmin && len(repoIDs) == 0 {
		repoIDs, err = c.Store.RunnerRepoIDs(key.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if len(repoIDs) == 0 {
			// Nothing attached: nothing to claim. Still a heartbeat, so
			// admin runners shows the key polling.
			c.Store.TouchRunner(key.ID, c.User.ID, "", 0)
			return c.emit(map[string]any{}, func(w io.Writer) { fmt.Fprintln(w, "no pending builds") })
		}
	}
	untrusted := f.Has("--untrusted")
	var b store.Build
	var repo store.Repo
	var ok bool
	for attempt := 0; attempt < maxOrphanSkip; attempt++ {
		b, ok, err = c.Store.ClaimBuild(repoIDs, untrusted)
```

The rest of the loop is unchanged. Delete the old `var err error` line, since `err` now comes from `parseFlags`. Replace the heartbeat line:

```go
	c.Store.TouchRunner(key.ID, c.User.ID, strings.Join(f.Pos, ","), b.ID)
```

In `runRunnerLog`, replace `if code := requireRunner(c); code >= 0 { return code }` with:

```go
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
```

and directly after the `id, err := strconv.ParseInt(args[0], 10, 64)` block, add:

```go
	if b, err := c.Store.BuildByID(id); err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	} else if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository")
	}
```

In `runRunnerDone`, the same `runnerSession` replacement; after `b, err := c.Store.BuildByID(id)` succeeds add:

```go
	if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository")
	}
```

and both `c.Store.RunnerDone(c.User.ID)` become `c.Store.RunnerDone(key.ID)`.

Delete `requireRunner` if nothing else references it (`grep -n requireRunner internal/`).

- [ ] **Step 4: `admin runners` shows the fingerprint and attachments**

In `internal/control/admin.go`, `runAdminRunners`, after `ListRunners`:

```go
	for i := range runners {
		if runners[i].Scope != "" {
			continue
		}
		key, err := c.Store.SSHKeyByID(runners[i].KeyID)
		if err != nil || key.Scope != "runner" {
			continue // an admin key with no -repos: any
		}
		paths, err := c.Store.RunnerRepoPaths(runners[i].KeyID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		runners[i].Scope = strings.Join(paths, ",")
	}
```

A runner key that asked for `-repos` shows that; one that did not shows its attachments. Both are what the key may claim. Text row:

```go
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Username, r.Fingerprint, r.LastSeen, scope, held)
```

Add `"strings"` to admin.go imports if missing.

- [ ] **Step 5: Fix the existing `runnerCtx` test helper**

`internal/control/runnernext_test.go`, `runnerCtx`: the session needs a real key. Replace the helper body:

```go
func runnerCtx(st *store.Store, uid int64, root string) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	fp := fmt.Sprintf("SHA256:runner-%d", uid)
	st.AddSSHKey(uid, fp, "ssh-ed25519", []byte(fp), "full") // ErrDuplicateKey on reuse is fine
	c := &Ctx{
		User:   store.User{ID: uid, Username: "ci", IsAdmin: true},
		Scope:  "full",
		Source: fp,
		Store:  st,
		Cfg:    config.Config{Server: config.Server{Root: root, SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
	}
	return c, &out
}
```

Any other control test that builds a `Ctx` for `runRunnerNext`, `runRunnerLog` or `runRunnerDone` (`grep -ln "runRunner" internal/control/*_test.go`) needs the same: an `AddSSHKey` and `Source` set to its fingerprint.

- [ ] **Step 6: Run the control tests**

Run: `go build ./... && go vet ./internal/control && go test ./internal/control 2>&1 | tail -5`
Expected: PASS, including `TestStdinCommandsReadStdin` and `TestReadOnlyCommandsWriteNothing`.

- [ ] **Step 7: Update the e2e tests that this changes**

`e2e/reap_test.go:112-115`: the row is now `ci<TAB>SHA256:...<TAB>last_seen<TAB>alice/app<TAB>idle`. The existing assertions `strings.Contains(out, "\nci\t")` and `strings.Contains(out, "\talice/app\tidle")` still hold. No change unless the test fails; run it in Step 8.

`e2e/mrbuilds_test.go:95` claims a fork head with an admin key. Add the flag:

```go
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--untrusted", "alice/app", "--json")
```

`e2e/build_cancel_test.go`: find every `"runner", "next"` that claims a merge request head from a fork (`grep -n 'runner", "next"' e2e/build_cancel_test.go`, and read the test around each). Add `"--untrusted"` right after `"next"` where the queued build is a fork head. Same-repository branches are trusted and need nothing.

- [ ] **Step 8: Run those e2e tests**

Run: `go test ./e2e -run 'TestStaleBuildReapedWithoutRunner|TestForkMRHeadIsBuilt|TestRunnerNextScopedToRepos|TestRunnerScopedKey' -count=1 2>&1 | tail -5`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/control/build.go internal/control/admin.go internal/control/runnernext_test.go internal/control/runnerattach_test.go e2e/reap_test.go e2e/mrbuilds_test.go e2e/build_cancel_test.go
git commit -m "control: a runner key claims only the repositories it is attached to

runner next takes --untrusted; without it fork heads are skipped. runner
log and runner done refuse a build outside the key's attachments. The
heartbeat and admin runners are per key.

Ref #184"
```

---

### Task 4: Control and CLI: `repo runner add|list|remove`

**Files:**
- Create: `internal/control/runnerrepo.go`
- Create: `internal/control/runnerrepo_test.go`
- Modify: `cmd/gitbay/main.go:437-440` (a `group("runner", ...)` beside `deploy-key`)

**Interfaces:**
- Consumes: the store functions from Task 2; `resolveRepo(c, path, policy.CanAdmin)`; `c.Store.Audit(userID, action string, fields map[string]any)`.
- Produces:
  - `repo runner add <owner/name>` (stdin: public key) → `{"fingerprint": ..., "repo": ...}`
  - `repo runner list <owner/name>` → `[]store.RepoRunner`
  - `repo runner remove <owner/name> <fingerprint>` → `{"removed": fingerprint}`

- [ ] **Step 1: Write the failing tests**

`internal/control/runnerrepo_test.go`:

```go
package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Generated once with ssh-keygen -t ed25519; a valid authorized_keys line.
const testRunnerPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILAr2r82jFsCJwsEyrEf2wgKy9Dv45xYYici6Ii7NyCS runner@test\n"

func repoRunnerCtx(t *testing.T, st *store.Store, uid int64, admin bool, stdin string) (*Ctx, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return &Ctx{
		User:   store.User{ID: uid, Username: "alice", IsAdmin: admin},
		Scope:  "full",
		Source: "SHA256:session",
		Store:  st,
		Cfg:    config.Config{Server: config.Server{SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &out,
		JSON:   true,
	}, &out
}

// A fresh key is registered on the caller's account with scope runner and
// attached; a second add is a no-op; list shows it; remove detaches and
// leaves the key on the account.
func TestRepoRunnerAddListRemove(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	c, out := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("add: exit %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"fingerprint":"SHA256:`) {
		t.Fatalf("add output: %s", out.String())
	}
	keys, _ := st.ListSSHKeys(uid)
	if len(keys) != 1 || keys[0].Scope != "runner" {
		t.Fatalf("key not registered as runner: %+v", keys)
	}
	fp := keys[0].Fingerprint
	c, out = repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("second add: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerList(c, []string{repo.Path()}); code != protocol.ExitOK || strings.Count(out.String(), fp) != 1 {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerRemove(c, []string{repo.Path(), fp}); code != protocol.ExitOK {
		t.Fatalf("remove: exit %d %s", code, out.String())
	}
	if ok, _ := st.RunnerAttached(keys[0].ID, repo.ID); ok {
		t.Fatal("still attached after remove")
	}
	if keys, _ = st.ListSSHKeys(uid); len(keys) != 1 {
		t.Fatal("remove dropped the key from the account")
	}
	c, out = repoRunnerCtx(t, st, uid, false, "")
	if code := runRepoRunnerRemove(c, []string{repo.Path(), fp}); code != protocol.ExitNotFound {
		t.Fatalf("remove twice: exit %d, want %d", code, protocol.ExitNotFound)
	}
}

// A key that already exists with another scope is never promoted, and
// another account's runner key is refused unless the caller is an admin.
func TestRepoRunnerAddRefusesWrongKeys(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	c, _ := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	// Register the same key as a full key first.
	if code := runKeysAdd(c, nil); code != protocol.ExitOK {
		t.Fatal("keys add failed")
	}
	c, out := repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitDenied {
		t.Fatalf("full key accepted as runner: exit %d %s", code, out.String())
	}
	keys, _ := st.ListSSHKeys(uid)
	if keys[0].Scope != "full" {
		t.Fatalf("scope changed to %s", keys[0].Scope)
	}
	// Someone else's runner key.
	bob, _ := st.CreateUser("bob", false)
	if err := st.AddSSHKey(bob, "SHA256:bobrunner", "ssh-ed25519", []byte("x"), "runner"); err != nil {
		t.Fatal(err)
	}
	st.RemoveSSHKey(uid, keys[0].Fingerprint)
	if err := st.AddSSHKey(bob, keys[0].Fingerprint, "ssh-ed25519", keys[0].Blob, "runner"); err != nil {
		t.Fatal(err)
	}
	c, out = repoRunnerCtx(t, st, uid, false, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitDenied {
		t.Fatalf("another account's key attached by a non-admin: exit %d %s", code, out.String())
	}
	c, out = repoRunnerCtx(t, st, uid, true, testRunnerPub)
	if code := runRepoRunnerAdd(c, []string{repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("admin could not attach another account's runner key: exit %d %s", code, out.String())
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/control -run TestRepoRunner 2>&1 | head -5`
Expected: `undefined: runRepoRunnerAdd`.

- [ ] **Step 3: Write `internal/control/runnerrepo.go`**

```go
package control

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Runners attached to a repository (#184). A runner key claims builds only
// for the repositories it is attached to; a repository admin attaches it
// by pasting the runner's public key. The key lands on the admin's own
// account with scope runner, which confines it to the runner protocol and
// read-only git.
func init() {
	register(Command{Path: []string{"repo", "runner", "add"},
		Summary:    "attach a runner's public key to a repository",
		Usage:      "repo runner add <owner/name> < key.pub",
		ReadsStdin: true, Run: runRepoRunnerAdd})
	register(Command{Path: []string{"repo", "runner", "list"},
		Summary: "list the runners attached to a repository",
		Usage:   "repo runner list <owner/name>", ReadOnly: true, Run: runRepoRunnerList})
	register(Command{Path: []string{"repo", "runner", "remove"},
		Summary: "detach a runner from a repository",
		Usage:   "repo runner remove <owner/name> <fingerprint>", Run: runRepoRunnerRemove})
}

func runRepoRunnerAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{MaxPos: 1, Usage: "repo runner add <owner/name> < key.pub"})
	if err != nil || len(f.Pos) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo runner add <owner/name> < key.pub")
	}
	repo, code := resolveRepo(c, f.Pos[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading key: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return c.fail(protocol.ExitUsage, "not a valid public key in authorized_keys format: %v", err)
	}
	fp := ssh.FingerprintSHA256(pub)
	key, err := c.Store.SSHKeyByFingerprint(fp)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if err := c.Store.AddSSHKey(c.User.ID, fp, pub.Type(), pub.Marshal(), "runner"); err != nil {
			return c.fail(protocol.ExitFailure, "adding key: %v", err)
		}
		if key, err = c.Store.SSHKeyByFingerprint(fp); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	case err != nil:
		return c.fail(protocol.ExitFailure, "%v", err)
	case key.Scope != "runner":
		// A full key would let a build step administer the account; a
		// deploy key is bound elsewhere. A runner gets a key of its own.
		return c.fail(protocol.ExitDenied, "%s is a %s key, not a runner key; give the runner a key of its own", fp, key.Scope)
	case key.UserID != c.User.ID && !c.User.IsAdmin:
		return c.fail(protocol.ExitDenied, "%s belongs to another account", fp)
	}
	if err := c.Store.AttachRunner(key.ID, repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "repo.runner.add", map[string]any{"repo": repo.Path(), "fingerprint": fp})
	d := map[string]string{"fingerprint": fp, "repo": repo.Path()}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "runner %s attached to %s\n", fp, repo.Path())
	})
}

func runRepoRunnerList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo runner list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	runners, err := c.Store.ListRepoRunners(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if runners == nil {
		runners = []store.RepoRunner{}
	}
	return c.emit(runners, func(w io.Writer) {
		for _, r := range runners {
			seen := r.LastSeen
			if seen == "" {
				seen = "never"
			}
			held := "idle"
			if r.BuildNumber != 0 {
				held = fmt.Sprintf("%s #%d %s since %s", r.BuildRepo, r.BuildNumber, r.BuildJob, r.StartedAt)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Fingerprint, r.Algo, r.Username, seen, held)
		}
	})
}

func runRepoRunnerRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo runner remove <owner/name> <fingerprint>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.DetachRunner(repo.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no runner %s on %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "repo.runner.remove", map[string]any{"repo": repo.Path(), "fingerprint": args[1]})
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "runner %s detached from %s\n", args[1], repo.Path())
	})
}
```

`Store.Audit(actorID int64, action string, data map[string]any)` returns nothing.

- [ ] **Step 4: Add the CLI table entries**

In `cmd/gitbay/main.go`, directly after the `group("deploy-key", ...)` block (line 437-440):

```go
		group("runner", "runners attached to a repository",
			pass("add", "attach a runner's public key: < key.pub", passOpts{server: []string{"repo", "runner", "add"}, needsRepo: true, alwaysStdin: true, stdinWhat: "an SSH public key"}),
			pass("list", "list attached runners", passOpts{server: []string{"repo", "runner", "list"}, needsRepo: true}),
			pass("remove", "detach a runner: <fingerprint>", passOpts{server: []string{"repo", "runner", "remove"}, needsRepo: true}),
		),
```

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go test ./internal/control ./cmd/gitbay 2>&1 | tail -5`
Expected: PASS, including the CLI coverage test.

- [ ] **Step 6: Commit**

```bash
git add internal/control/runnerrepo.go internal/control/runnerrepo_test.go cmd/gitbay/main.go
git commit -m "control, cli: repo runner add, list, remove

Ref #184"
```

---

### Task 5: Web: Runners on the repository settings page

**Files:**
- Modify: `internal/httpd/settings.go:18-49` (`settingsPage`, `settingsForm`) and the `switch` in `settingsSubmit`
- Modify: `internal/web/templates/settings.html` (a section after Dependencies, before Lifecycle)
- Create: `e2e/runnerweb_test.go`

**Interfaces:**
- Consumes: `repo runner list|add|remove` from Task 4; `s.runControlInto`, `s.runControlStdin(u, argv, stdin) (msg string, ok bool)`, `s.runControl`.
- Produces: form fields `field=runner-add` with `key`, and `field=runner-remove` with `fingerprint`.

- [ ] **Step 1: Write the failing e2e test**

`e2e/runnerweb_test.go`:

```go
package e2e

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// The settings page attaches and detaches runners through the same
// commands the CLI uses, and lists what is attached.
func TestRunnerSettingsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	runnerKey := inst.newKey(t, "laptop")
	pub, _ := os.ReadFile(runnerKey + ".pub")

	alice := inst.login(t, aliceKey)
	settings := inst.base() + "/alice/app/settings"
	_, body := browserGet(t, alice, settings)
	if !strings.Contains(body, "No runners attached") {
		t.Fatalf("empty state missing:\n%s", body)
	}
	if status, _ := browserPost(t, alice, settings, url.Values{"field": {"runner-add"}, "key": {string(pub)}}); status != 200 {
		t.Fatalf("runner-add post: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json")
	if !strings.Contains(out, `"fingerprint":"SHA256:`) {
		t.Fatalf("not attached after the form: %s", out)
	}
	fp := out[strings.Index(out, "SHA256:"):]
	fp = fp[:strings.Index(fp, `"`)]
	_, body = browserGet(t, alice, settings)
	if !strings.Contains(body, fp) || !strings.Contains(body, `value="runner-remove"`) {
		t.Fatalf("attached runner not listed:\n%s", body)
	}
	if status, _ := browserPost(t, alice, settings, url.Values{"field": {"runner-remove"}, "fingerprint": {fp}}); status != 200 {
		t.Fatalf("runner-remove post: %d", status)
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json"); strings.Contains(out, fp) {
		t.Fatalf("still attached after remove: %s", out)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./e2e -run TestRunnerSettingsWeb -count=1 2>&1 | tail -5`
Expected: FAIL at "empty state missing".

- [ ] **Step 3: Handler changes in `internal/httpd/settings.go`**

`settingsPage` gains:

```go
	Runners     []store.RepoRunner
```

In `settingsForm`, after the deps read:

```go
	var runners []store.RepoRunner
	s.runControlInto(u, []string{"repo", "runner", "list", repo.Path()}, &runners)
```

and pass `Runners: runners` to the struct literal.

In `settingsSubmit`'s switch, before `default:`:

```go
	case "runner-add":
		body := v("key")
		if body == "" {
			s.settingsRedirect(w, r, "paste the runner's public key")
			return
		}
		msg, ok := s.runControlStdin(u, []string{"repo", "runner", "add", repo}, body+"\n")
		if ok {
			msg = ""
		}
		s.settingsRedirect(w, r, msg)
		return
	case "runner-remove":
		argv = []string{"repo", "runner", "remove", repo, v("fingerprint")}
```

- [ ] **Step 4: Template section**

In `internal/web/templates/settings.html`, before `<h2>Lifecycle</h2>`:

```html
<h2>Runners</h2>
{{if .Runners}}
<ul class="protlist">
{{range .Runners}}<li><code>{{.Fingerprint}}</code> <span class="meta">{{.Username}}{{if .LastSeen}}, last poll {{.LastSeen}}{{else}}, never polled{{end}}{{if .BuildNumber}}, running {{.BuildRepo}} #{{.BuildNumber}} {{.BuildJob}}{{end}}</span>
  <form method="post" action="{{$base}}" class="inline">
    <input type="hidden" name="field" value="runner-remove">
    <input type="hidden" name="fingerprint" value="{{.Fingerprint}}">
    <button type="submit" class="linklike">Detach</button>
  </form></li>
{{end}}
</ul>
{{else}}<p class="meta">No runners attached. Builds for this repository run on the runners attached here; a repository with none queues builds nothing claims.</p>{{end}}
<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="runner-add">
  <label for="runner-key">Attach a runner</label>
  <textarea id="runner-key" name="key" rows="3" placeholder="ssh-ed25519 AAAA… (from gitbay-runner init)"></textarea>
  <button type="submit">Attach</button>
</form>
<p class="meta">Install <code>gitbay-runner</code>, run <code>gitbay-runner init</code>, and paste the key it prints. The runner builds your commits with the repository's secrets; merge requests from forks wait unless it runs with <code>-untrusted</code>.</p>
```

- [ ] **Step 5: Run the test**

Run: `go test ./e2e -run TestRunnerSettingsWeb -count=1 2>&1 | tail -5`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/settings.go internal/web/templates/settings.html e2e/runnerweb_test.go
git commit -m "httpd: attach and detach runners on the settings page

Ref #184"
```

---

### Task 6: Runner: config file, `-identity`, `-untrusted`

**Files:**
- Create: `cmd/gitbay-runner/config.go`
- Create: `cmd/gitbay-runner/config_test.go`
- Modify: `cmd/gitbay-runner/main.go` (flag block, `runner` struct, `step`, ssh option assembly)

**Interfaces:**
- Produces:
  - `configDir() string`: `$XDG_CONFIG_HOME/gitbay-runner` or `$HOME/.config/gitbay-runner`.
  - `defaultConfigPath() string`: `configDir()/config.toml`.
  - `configPathFromArgs(args []string, def string) string`: honours `-config X`, `--config X`, `-config=X`.
  - `loadConfig(path string) (map[string]string, bool, error)`: flag name to value, false when the file is absent.
  - `applyConfig(fs *flag.FlagSet, values map[string]string) error`: `fs.Set` each.
  - `identityOpts(path string) []string`: `["-i", path, "-o", "IdentitiesOnly=yes"]` or nil.
  - Flags `-config`, `-identity`, `-untrusted`.

- [ ] **Step 1: Write the failing tests**

`cmd/gitbay-runner/config_test.go`:

```go
package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// A config file sets the flags' values; a flag on the command line wins.
func TestConfigFileFeedsFlagsAndFlagsOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("remote = \"git@example.test\"\npoll = \"9s\"\nuntrusted = true\nidentity = \"/k\"\njobs = 2\n"), 0o600)

	values, found, err := loadConfig(path)
	if err != nil || !found {
		t.Fatalf("loadConfig: found=%v err=%v", found, err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	remote := fs.String("remote", "git@gitbay.org", "")
	poll := fs.Duration("poll", 0, "")
	untrusted := fs.Bool("untrusted", false, "")
	identity := fs.String("identity", "", "")
	jobs := fs.Int("jobs", 1, "")
	if err := applyConfig(fs, values); err != nil {
		t.Fatal(err)
	}
	if err := fs.Parse([]string{"-poll", "3s"}); err != nil {
		t.Fatal(err)
	}
	if *remote != "git@example.test" || poll.String() != "3s" || !*untrusted || *identity != "/k" || *jobs != 2 {
		t.Fatalf("remote=%s poll=%s untrusted=%v identity=%s jobs=%d", *remote, poll, *untrusted, *identity, *jobs)
	}
	if _, found, err := loadConfig(filepath.Join(dir, "missing.toml")); found || err != nil {
		t.Fatalf("missing file: found=%v err=%v", found, err)
	}
	if _, _, err := loadConfig(path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("nonsense = \"x\"\n"), 0o600)
	if _, _, err := loadConfig(path); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "/def"},
		{[]string{"-once"}, "/def"},
		{[]string{"-config", "/a"}, "/a"},
		{[]string{"--config", "/b", "-once"}, "/b"},
		{[]string{"-config=/c"}, "/c"},
	} {
		if got := configPathFromArgs(tc.args, "/def"); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.args, got, tc.want)
		}
	}
}

func TestConfigDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if got := configDir(); got != "/x/gitbay-runner" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if got := configDir(); got != "/h/.config/gitbay-runner" {
		t.Fatalf("got %s", got)
	}
}

func TestIdentityOpts(t *testing.T) {
	if got := identityOpts(""); got != nil {
		t.Fatalf("empty identity produced %v", got)
	}
	got := identityOpts("/k")
	if len(got) != 4 || got[0] != "-i" || got[1] != "/k" || got[3] != "IdentitiesOnly=yes" {
		t.Fatalf("got %v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./cmd/gitbay-runner -run 'TestConfig|TestIdentity' 2>&1 | head -5`
Expected: `undefined: loadConfig` and friends.

- [ ] **Step 3: Write `cmd/gitbay-runner/config.go`**

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// The runner takes everything as flags, which does not work under a
// service manager. config.toml in the config directory carries the same
// names; a flag on the command line overrides it (#184).

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gitbay-runner")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "gitbay-runner")
}

func defaultConfigPath() string { return filepath.Join(configDir(), "config.toml") }

// configPathFromArgs finds -config before the flag set is parsed, since
// the file's values must be set before parsing for flags to override them.
func configPathFromArgs(args []string, def string) string {
	for i, a := range args {
		a = strings.TrimPrefix(a, "-")
		if a == "-config" || a == "config" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if v, ok := strings.CutPrefix(a, "config="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "-config="); ok {
			return v
		}
	}
	return def
}

// configKeys is every key the file may carry: the flag names.
var configKeys = map[string]bool{"remote": true, "ssh-opts": true, "clone-base": true, "workdir": true,
	"poll": true, "timeout": true, "repos": true, "jobs": true, "image": true, "isolation": true,
	"memory": true, "cpus": true, "untrusted": true, "identity": true}

// loadConfig reads path into flag name → value. Absent file: found is
// false and there is no error. An unknown key is an error, not a typo
// the runner silently ignores.
func loadConfig(path string) (values map[string]string, found bool, err error) {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	values = map[string]string{}
	for k, v := range raw {
		if !configKeys[k] {
			return nil, true, fmt.Errorf("%s: unknown key %s", path, k)
		}
		values[k] = fmt.Sprint(v)
	}
	return values, true, nil
}

// applyConfig sets each value on the flag set, which is what parsing the
// command line would do; parse afterwards and the command line wins.
func applyConfig(fs *flag.FlagSet, values map[string]string) error {
	for k, v := range values {
		if fs.Lookup(k) == nil {
			return fmt.Errorf("config: unknown key %s", k)
		}
		if err := fs.Set(k, v); err != nil {
			return fmt.Errorf("config: %s: %w", k, err)
		}
	}
	return nil
}

// identityOpts is what makes ssh and git use the runner's own key and no
// other: on a laptop the ambient key is the user's full-scope one, which
// the runner protocol refuses.
func identityOpts(path string) []string {
	if path == "" {
		return nil
	}
	return []string{"-i", path, "-o", "IdentitiesOnly=yes"}
}
```

- [ ] **Step 4: Wire it into `main.go`**

In `main()`, replace `flag.Parse()` and the flag block with a flag set fed by the config file. Add three flags and keep the others as they are:

```go
	var (
		configPath = flag.String("config", defaultConfigPath(), "config file; keys are these flag names, flags override it")
		identity   = flag.String("identity", "", "ssh private key to poll and clone with (default: the key gitbay-runner init generated, if present)")
		untrusted  = flag.Bool("untrusted", false, "also claim untrusted builds: merge request heads from forks (needs -isolation podman to be safe)")
		// ... existing flags unchanged ...
	)
	path := configPathFromArgs(os.Args[1:], *configPath)
	if values, found, err := loadConfig(path); err != nil {
		log.Fatal(err)
	} else if found {
		if err := applyConfig(flag.CommandLine, values); err != nil {
			log.Fatal(err)
		}
		log.Printf("config: %s", path)
	}
	flag.Parse()
```

After `if *sshOpts != "" { r.sshOpts = strings.Fields(*sshOpts) }`:

```go
	if *identity == "" {
		if p := filepath.Join(configDir(), "id_ed25519"); fileExists(p) {
			*identity = p
		}
	}
	r.sshOpts = append(identityOpts(*identity), r.sshOpts...)
	r.untrusted = *untrusted
```

with

```go
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
```

`runner` struct gains `untrusted bool`. In `step()`:

```go
	args := []string{"runner", "next"}
	if r.untrusted {
		args = append(args, "--untrusted")
	}
	args = append(append(args, r.repos...), "--json")
	out, err := r.ssh(nil, args...)
```

Both `ssh()` and the `gitSSH` line already use `r.sshOpts`, so the identity reaches both.

Move the `init` dispatch hook in now so Task 7 has a place to land, at the top of `main()`:

```go
	if len(os.Args) > 1 && os.Args[1] == "init" {
		os.Exit(runInit(os.Args[2:]))
	}
```

and a stub in `config.go` until Task 7 replaces it:

```go
func runInit(args []string) int { fmt.Fprintln(os.Stderr, "init: not implemented"); return 2 }
```

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./cmd/gitbay-runner && go test ./cmd/gitbay-runner 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/gitbay-runner/config.go cmd/gitbay-runner/config_test.go cmd/gitbay-runner/main.go
git commit -m "runner: config.toml, -identity, -untrusted

Ref #184"
```

---

### Task 7: Runner: `gitbay-runner init`

**Files:**
- Create: `cmd/gitbay-runner/init.go` (replaces the stub `runInit` in `config.go`; delete the stub)
- Create: `cmd/gitbay-runner/init_test.go`

**Interfaces:**
- Consumes: `configDir()`, `defaultWorkdir()`, `toolpath.Look("ssh-keygen")`.
- Produces: `runInit(args []string) int`; files `<configDir>/id_ed25519`, `id_ed25519.pub`, `config.toml`.

- [ ] **Step 1: Write the failing test**

`cmd/gitbay-runner/init_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// init creates the key and config once, prints the key and the attach
// command, and running it again changes nothing.
func TestInitWritesKeyAndConfigOnce(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	var out bytes.Buffer
	initOut = &out
	defer func() { initOut = os.Stdout }()

	if code := runInit([]string{"-remote", "git@example.test"}); code != 0 {
		t.Fatalf("init: exit %d\n%s", code, out.String())
	}
	cdir := filepath.Join(dir, "gitbay-runner")
	key := filepath.Join(cdir, "id_ed25519")
	pub, err := os.ReadFile(key + ".pub")
	if err != nil || !strings.HasPrefix(string(pub), "ssh-ed25519 ") {
		t.Fatalf("public key: %v %q", err, pub)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %o", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(cdir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode %o", fi.Mode().Perm())
	}
	cfg, _ := os.ReadFile(filepath.Join(cdir, "config.toml"))
	for _, want := range []string{"remote = \"git@example.test\"", "isolation = \"none\"", "untrusted = false", "identity = \"" + key + "\""} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config lacks %q:\n%s", want, cfg)
		}
	}
	for _, want := range []string{strings.TrimSpace(string(pub)), "gitbay repo runner add owner/name < " + key + ".pub", "https://example.test/owner/name/settings"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := runInit([]string{"-remote", "git@other.test"}); code != 0 {
		t.Fatalf("second init: exit %d\n%s", code, out.String())
	}
	if pub2, _ := os.ReadFile(key + ".pub"); string(pub2) != string(pub) {
		t.Fatal("second init replaced the key")
	}
	if cfg2, _ := os.ReadFile(filepath.Join(cdir, "config.toml")); string(cfg2) != string(cfg) {
		t.Fatal("second init rewrote the config")
	}
}

// podman needs an image; init refuses to write a config the runner would
// refuse to start with.
func TestInitPodmanNeedsImage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	initOut = &out
	defer func() { initOut = os.Stdout }()
	if code := runInit([]string{"-isolation", "podman"}); code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out.String())
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./cmd/gitbay-runner -run TestInit 2>&1 | head -5`
Expected: `undefined: initOut`.

- [ ] **Step 3: Write `cmd/gitbay-runner/init.go`**

```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// initOut is where init prints; tests capture it.
var initOut io.Writer = os.Stdout

// runInit makes a fresh install ready to attach: a key of its own, a
// config file the service reads, and the one command to run next. It never
// overwrites a key or a config that exists, so running it twice is safe.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(initOut)
	remote := fs.String("remote", "git@gitbay.org", "ssh destination of the gitbay server")
	workdir := fs.String("workdir", defaultWorkdir(), "build workspace root")
	isolation := fs.String("isolation", isolationNone, "how steps run: none, or podman with -image")
	image := fs.String("image", "", "container image for -isolation podman")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *isolation == isolationPodman && *image == "" {
		fmt.Fprintln(initOut, "-isolation podman needs -image <ref>: the runner refuses to start without one, and there is no image to guess")
		return 2
	}
	if *isolation != isolationPodman && *isolation != isolationNone {
		fmt.Fprintf(initOut, "unknown isolation %q\n", *isolation)
		return 2
	}

	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(initOut, err)
		return 1
	}
	os.Chmod(dir, 0o700)
	key := filepath.Join(dir, "id_ed25519")
	if !fileExists(key) {
		cmd := exec.Command(toolpath.Look("ssh-keygen"), "-q", "-t", "ed25519", "-N", "", "-C", "gitbay-runner", "-f", key)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(initOut, "ssh-keygen: %v\n%s", err, out)
			return 1
		}
	}
	os.Chmod(key, 0o600)

	cfgPath := filepath.Join(dir, "config.toml")
	if !fileExists(cfgPath) {
		var b strings.Builder
		fmt.Fprintf(&b, "remote = %q\n", *remote)
		fmt.Fprintf(&b, "workdir = %q\n", *workdir)
		fmt.Fprintf(&b, "isolation = %q\n", *isolation)
		if *image != "" {
			fmt.Fprintf(&b, "image = %q\n", *image)
		}
		fmt.Fprintf(&b, "untrusted = false\n")
		fmt.Fprintf(&b, "identity = %q\n", key)
		if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
			fmt.Fprintln(initOut, err)
			return 1
		}
	}

	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		fmt.Fprintln(initOut, err)
		return 1
	}
	host := *remote
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	fmt.Fprintf(initOut, "config: %s\nkey:    %s\n\n", cfgPath, key)
	if *isolation == isolationNone {
		fmt.Fprintln(initOut, "Steps run on this machine as your user, with no container. Untrusted builds\n(merge requests from forks) are excluded unless the runner is started with\n-untrusted, so that means your own commits.\n")
	}
	fmt.Fprintf(initOut, "This runner's public key:\n\n  %s\nAttach it to each repository it should build, as a repository admin:\n\n  gitbay repo runner add owner/name < %s.pub\n\nor paste it under Runners at https://%s/owner/name/settings\n\nThen start it:\n\n  brew services start krz/tap/gitbay-runner\n\nor run gitbay-runner with no arguments.\n",
		strings.TrimSpace(string(pub)), key, host)
	return 0
}
```

`isolationNone` and `isolationPodman` are the constants in `isolate.go:20-21`. Delete the stub `runInit` from `config.go`.

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go vet ./cmd/gitbay-runner && go test ./cmd/gitbay-runner 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbay-runner/init.go cmd/gitbay-runner/init_test.go cmd/gitbay-runner/config.go
git commit -m "runner: init generates the key and config and prints the attach step

Ref #184"
```

---

### Task 8: e2e: init, attach, build; fork head waits

**Files:**
- Create: `e2e/runnerattach_test.go`

**Interfaces:**
- Consumes: `buildRunner(t)`, `inst.newKey`, `inst.admin`, `inst.ssh(t, key, stdin, argv...)`, `inst.gitEnv`, `inst.sshURL`, `mustGit`, `inst.port`, `inst.sshDir` (all in `e2e/ci_test.go` and the instance helpers).

- [ ] **Step 1: Write the test**

`e2e/runnerattach_test.go`:

```go
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole flow a user goes through: init on their machine, attach the
// printed key to their repository, start the runner from the config init
// wrote. The runner builds their push and leaves a fork's merge request
// head alone until started with -untrusted.
func TestAttachedRunnerBuildsOwnRepo(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// init on "alice's laptop".
	xdg := t.TempDir()
	initCmd := exec.Command(inst.runner, "init", "-remote", "git@127.0.0.1")
	initCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg)
	initOut, err := initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v\n%s", err, initOut)
	}
	cdir := filepath.Join(xdg, "gitbay-runner")
	pub, err := os.ReadFile(filepath.Join(cdir, "id_ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(initOut), strings.TrimSpace(string(pub))) {
		t.Fatalf("init did not print the key:\n%s", initOut)
	}

	// The unattached key claims nothing, even with a build queued.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo built\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	run := func(extra ...string) string {
		t.Helper()
		// No -i in ssh-opts: the identity from the config is what
		// authenticates, which is the point.
		opts := fmt.Sprintf("-p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
			inst.port, filepath.Join(inst.sshDir, "known_hosts"))
		args := append([]string{"-config", filepath.Join(cdir, "config.toml"), "-once",
			"-ssh-opts", opts,
			"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", inst.port),
			"-workdir", t.TempDir()}, extra...)
		cmd := exec.Command(inst.runner, args...)
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("runner: %v\n%s", err, out)
		}
		return string(out)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); !strings.Contains(out, "unit\tpending") {
		t.Fatalf("build not pending before attach: %s", out)
	}

	// Attach with the printed key.
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "repo", "runner", "add", "alice/app"); code != 0 {
		t.Fatalf("repo runner add: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json")
	if !strings.Contains(out, `"username":"alice"`) || strings.Contains(out, `"last_seen":"20`) {
		t.Fatalf("list after attach: %s", out)
	}

	// The runner builds it.
	run()
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); !strings.Contains(out, "unit\tsuccess") {
		t.Fatalf("build not built by the attached runner: %s", out)
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json"); !strings.Contains(out, `"last_seen":"20`) {
		t.Fatalf("no heartbeat after a poll: %s", out)
	}

	// bob forks and opens a merge request: an untrusted build in the target.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/app"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	bwork := t.TempDir()
	benv := inst.gitEnv(bobKey)
	mustGit(t, bwork, benv, "clone", inst.sshURL("bob/app"), "w")
	bdir := filepath.Join(bwork, "w")
	mustGit(t, bdir, benv, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(bdir, "f.txt"), []byte("y\n"), 0o644)
	mustGit(t, bdir, benv, "add", ".")
	mustGit(t, bdir, benv, "commit", "-q", "-m", "change")
	mustGit(t, bdir, benv, "push", "-q", "origin", "feat")
	if _, _, code := inst.ssh(t, bobKey, "", "build", "cancel", "bob/app", "1"); code != 0 {
		t.Fatal("cancel bob's own build")
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "bob/app:feat", "--target", "main", "--title", "change"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	run()
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); strings.Count(out, "pending") != 1 {
		t.Fatalf("fork head was claimed without -untrusted:\n%s", out)
	}
	run("-untrusted")
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); strings.Contains(out, "pending") {
		t.Fatalf("fork head not built with -untrusted:\n%s", out)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./e2e -run TestAttachedRunnerBuildsOwnRepo -count=1 -v 2>&1 | tail -20`
Expected: PASS. If the fork's build numbering differs (two jobs, or the fork's push queues more than one build), adjust the `build cancel bob/app` loop to cancel every pending build listed by `build list bob/app --json`.

- [ ] **Step 3: Commit**

```bash
git add e2e/runnerattach_test.go
git commit -m "e2e: init, attach, and an attached runner building its repository

Ref #184"
```

---

### Task 9: Docs: wiki pages

**Files:**
- Modify: `.gitbay/wiki/Users.org` (after the "CI builds" section's last paragraph, before "* Large files (LFS)")
- Modify: `.gitbay/wiki/Admin.org:329-340` and `:395-402`
- Modify: `.gitbay/wiki/Threat-Model.org:120-126`
- Modify: `.gitbay/wiki/Parity.org` (repo table, after the `webhooks` row; and the "SSH only, by design" paragraph is unchanged)
- Modify: `.gitbay/wiki/FAQ.org:20-25`
- Modify: `.gitbay/wiki/CI.org` (one sentence after the three mechanisms list)

- [ ] **Step 1: Users: "Your own runner"**

Insert before `* Large files (LFS)`:

```org
** Your own runner

Builds run on runners attached to the repository. An instance need not
offer any: install =gitbay-runner= on a machine of yours and attach it.

#+begin_src sh
brew install krz/tap/gitbay-runner        # or a binary from the release
gitbay-runner init -remote git@gitbay.org
#+end_src

=init= generates a key under =~/.config/gitbay-runner/=, writes
=config.toml= beside it, and prints the public key with the command to
attach it:

#+begin_src sh
gitbay repo runner add owner/name < ~/.config/gitbay-runner/id_ed25519.pub
#+end_src

or paste the key under Runners on the repository's settings page. Then
=brew services start krz/tap/gitbay-runner=, or run =gitbay-runner= with
no arguments; it reads the config file, and any flag overrides it.

What it builds: every build for the repositories it is attached to,
with the repository's secrets, and nothing else. Merge requests from
forks are untrusted and wait unless the runner runs with =-untrusted=,
which is only sensible with =-isolation podman -image <ref>= (see
[[Admin][Admin]]). Attach one runner to several repositories by repeating
=repo runner add=; run several runners on one account by running =init=
on each machine. =repo runner list= shows each attached key, when it
last polled and the build it holds; =repo runner remove <fingerprint>=
detaches one (the key stays on your account; =keys remove= drops it).
A runner key reaches only the runner protocol and read-only git, so a
build step that reads it off disk cannot administer your account.
```

- [ ] **Step 2: Admin**

Replace lines 329-340's opening paragraph ("=gitbay-runner= executes builds ... then removed:") with:

```org
=gitbay-runner= executes builds queued by pushes and merge requests. It
polls over SSH with a key of scope =runner=, which reaches only the
runner protocol and read-only git (a runner executes arbitrary
repository code, so the key it holds must not do more). A runner key
claims builds only for the repositories it is attached to, by =repo
runner add= from a repository admin or an instance admin; an admin key
claims any. Users attach their own runners: see the Users page. For an
instance runner, run it as a dedicated unprivileged user on a non-admin
account. =admin user create --key= registers a full-scope key, so the
runner key is added afterwards through a bootstrap key that is then
removed, and attached to each repository it should build:
```

After the existing bootstrap code block, add:

```org
#+begin_src sh
gitbay repo runner add krz/site < /var/lib/gitbay-runner/.ssh/id_ed25519.pub
#+end_src
```

Replace lines 395-402 (from "and the isolation canary, nothing else" to "the boundary is you choosing how to start it.") with:

```org
and the isolation canary, nothing else, because it shares the host with
the forge; any other repository builds on a runner its owner attaches.

=-repos= narrows an admin runner; for a runner key the attachments are
the boundary, held by the server, and =-repos= may only name
repositories among them. =-untrusted= makes a runner claim merge
request heads from forks; the bay1 unit sets it because it isolates in
podman. A runner without it builds trusted commits only.
```

Add `-untrusted` to the `gitbay-runner -remote git@gitbay.org -repos krz/site,krz/docs` example only if that runner isolates; leave the example as is and note under it: "Add =-untrusted= only with =-isolation podman=."

- [ ] **Step 3: Threat-Model**

Replace the "What the runner holds" bullet (lines 120-126) with:

```org
- *What the runner holds.* A key of scope =runner=, which the dispatcher
  confines to =runner next=, =runner log= and =runner done= and to
  read-only git, and which claims, logs and finishes builds only for
  the repositories it is attached to (=repo runner add=). A step that
  reads the key off the disk gets exactly that: it cannot administer
  the instance, push, read a repository the runner's account cannot, or
  touch another repository's builds. An admin key still works for the
  runner protocol so an operator can rotate at their own pace; a runner
  host should not hold one. Untrusted builds are skipped unless the
  runner asks with =-untrusted=, so a runner on a user's machine never
  executes a stranger's branch by default.
```

- [ ] **Step 4: Parity, FAQ, CI**

Parity, repo table, after the `webhooks` row:

```org
| runners attach, list, detach | yes | yes | no  |
```

The columns are cli, web, ios. iOS is `no`: outstanding, not intended.

FAQ, replace the "Does CI run for my repository on gitbay.org?" answer:

```org
- Does CI run for my repository on gitbay.org? :: On a runner you
  attach. The instance's own runner builds the forge's repositories and
  its isolation canary, since it shares the host with the forge.
  Install =gitbay-runner= on a machine of yours, run =gitbay-runner
  init=, and attach the key it prints with =repo runner add= or on the
  repository's settings page; see the Users page. A self-hosted
  instance can do the same, or run one runner for whichever
  repositories its operator attaches it to.
```

CI.org, after the three-mechanism list:

```org
Which runner takes a build is the fourth: a build is claimed only by a
runner attached to its repository (or an instance admin's runner), and
an untrusted build only by one started with =-untrusted=. A repository
with no runner attached queues builds nothing claims. See the Users
page.
```

- [ ] **Step 5: Check the wiki tests**

Run: `go test ./internal/hookd ./internal/ci -run 'Wiki|Parity' 2>&1 | tail -3`
Expected: PASS (nothing here changes the push-shape table).

- [ ] **Step 6: Commit**

```bash
git add .gitbay/wiki/Users.org .gitbay/wiki/Admin.org .gitbay/wiki/Threat-Model.org .gitbay/wiki/Parity.org .gitbay/wiki/FAQ.org .gitbay/wiki/CI.org
git commit -m "wiki: runners attached to repositories

Ref #184"
```

---

### Task 10: Deploy, release, and the tap

**Files:**
- Modify: `deploy/gitbay-runner.override.conf` (the `ExecStart` line)
- Modify: `deploy/release.sh:22` (the binary list)
- Create in `krz/homebrew-tap` (separate clone, after the release is tagged): `Formula/gitbay-runner.rb`; modify `Formula/gitbay.rb`

- [ ] **Step 1: The bay1 unit claims fork heads**

In `deploy/gitbay-runner.override.conf`, the `ExecStart=` line gains ` -untrusted` at the end, and the comment above `ExecStart` gains:

```
# -untrusted: this runner isolates in podman, so it takes merge request
# heads from forks; a runner without a container must not.
```

- [ ] **Step 2: Release binaries include the runner**

`deploy/release.sh`: `for bin in gitbay gitbayd; do` becomes `for bin in gitbay gitbayd gitbay-runner; do`, and the header comment's "gitbay and gitbayd" becomes "gitbay, gitbayd and gitbay-runner".

- [ ] **Step 3: Build, vet, and the touched unit tests**

Run: `go build ./... && go vet ./... && go test ./internal/store ./internal/control ./cmd/gitbay ./cmd/gitbay-runner ./internal/httpd 2>&1 | tail -8`
Expected: all PASS.

- [ ] **Step 4: Commit and open the MR**

```bash
git add deploy/gitbay-runner.override.conf deploy/release.sh
git commit -m "deploy: the bay1 runner claims untrusted builds; release ships gitbay-runner

Ref #184"
git push -u origin user-runners
gitbay mr create --source user-runners --target main --title "Runners attached to repositories" --file - <<'MR'
A runner key claims builds only for the repositories it is attached to
(`repo runner add|list|remove`, also on the settings page). `runner next`
skips untrusted builds unless `--untrusted`. Migration 0050.
`gitbay-runner init`, `config.toml`, `-identity`, `-untrusted`.

After deploy, attach the bay1 runner to krz/gitbay and cmc/ci-smoke as
the admin; until then it claims nothing.

Ref #184
MR
```

Wait for CI on bay1 (`gitbay build list` on the MR head) before merging: `gitbay mr merge <n> --strategy ff`.

- [ ] **Step 5: Deploy and attach (operator, after merge)**

```bash
make deploy && make deploy-runner
ssh -p 2222 root@46.232.248.67 cat /var/lib/gitbay-runner/.ssh/id_ed25519.pub | gitbay repo runner add krz/gitbay
ssh -p 2222 root@46.232.248.67 cat /var/lib/gitbay-runner/.ssh/id_ed25519.pub | gitbay repo runner add cmc/ci-smoke
gitbay admin runners
```

The last line must show the `ci` row with its fingerprint and `krz/gitbay,cmc/ci-smoke`.

- [ ] **Step 6: The tap, after the release is tagged**

In a clone of `https://gitbay.org/krz/homebrew-tap.git`, `Formula/gitbay-runner.rb`:

```ruby
class GitbayRunner < Formula
  desc "CI runner for gitbay: builds the repositories you attach it to"
  homepage "https://gitbay.org/krz/gitbay"
  url "https://gitbay.org/krz/gitbay.git",
      tag:      "v1.17.0",
      revision: "<commit of the tag>"
  license "0BSD"
  head "https://gitbay.org/krz/gitbay.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/gitbay-runner"
  end

  service do
    run [opt_bin/"gitbay-runner"]
    keep_alive true
    log_path var/"log/gitbay-runner.log"
    error_log_path var/"log/gitbay-runner.err.log"
  end

  def caveats
    <<~EOS
      Generate this machine's key and config, and print the key to attach:
        gitbay-runner init -remote git@gitbay.org
      Attach it to each repository it should build (as a repository admin):
        gitbay repo runner add owner/name < ~/.config/gitbay-runner/id_ed25519.pub
      Then:
        brew services start krz/tap/gitbay-runner
    EOS
  end

  test do
    assert_match "gitbay-runner", shell_output("#{bin}/gitbay-runner -version")
  end
end
```

In `Formula/gitbay.rb`, set `tag:` and `revision:` to the same release. Check with `brew install --build-from-source krz/tap/gitbay-runner && brew test krz/tap/gitbay-runner`, then commit on a branch of the tap and merge with an MR there.
