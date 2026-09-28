# Credentials and sessions implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Revocation of an SSH key takes effect on open connections
(#256); an expiring credential cannot mint one that outlives it, and
credentials record the token that made them (#257); SSH and deploy
keys take an optional expiry and show their last use (#277); browser
sessions end after 12 hours idle (#276); `web login` over SSH spends
the login-link budget (#278).

**Architecture:** The store announces revocations it commits
(`Store.OnRevoke`); the SSH listener tracks which key opened each
connection and cuts the ones a revocation names, killing a git
transport's process group. A 15-second sweep catches revocations made
by another process and keys that expire while connected. Every exec
re-reads its key. `Command.MintsCredential` marks the commands that
create credentials; `Dispatch` refuses them when `Ctx.Expires` is set.
`api_tokens` and `ssh_keys` gain `created_by_token`; `ssh_keys` gains
`expires_at`; `web_sessions` gains a sliding expiry under an absolute
cap.

**Tech stack:** Go, `golang.org/x/crypto/ssh`, SQLite (modernc), OpenSSH
client for e2e.

**Spec:** issues #256, #257, #276, #277, #278 on krz/gitbay (the
decisions on #256 and #257 are recorded there), and the brief
`/private/tmp/claude-501/-Users-cmc-git-krz-gitbay/7b2f1ea4-aab6-44e2-b2ad-d4ec6852ce42/scratchpad/brief.md`.

## Global constraints

- Each MR on its own branch off `main`. Commits are signed (the repo
  refuses unsigned), messages reference issues (`Ref #N`, and
  `Closes #N` on the commit that finishes one). No attribution to any
  assistant, model or AI anywhere: commits, MR bodies, comments.
- MR: `gitbay mr create --source <branch> --target main --title "..."`;
  merge with `gitbay mr merge <n> --strategy ff` once CI is green, then
  delete the branch locally and on the remote. Behind main → rebase,
  force-push, merge again.
- Locally: `go build ./...`, `go vet ./...`, unit tests of touched
  packages, and at most the one e2e test being written
  (`go test ./e2e -run TestName -count=1`). CI on bay1 runs the full suite.
- Registries that fail CI when a new thing lacks its row: top-level route
  word in `internal/policy/names.go`; new page template in the width map
  of `TestMainWidthClass` (`internal/web/web_test.go`); new `ReadOnly`
  command in `readArgs` in `e2e/readonly_test.go`; new control command
  needs a `pass()` entry in `cmd/gitbay/main.go` (coverage test);
  a command reading stdin needs `ReadsStdin: true`. This plan adds no
  route, template, command or read command; it changes flags and a
  `Summary` of none, so `cmd/gitbay/summaries_gen.go` stays current.
- Migrations: this plan owns 0060–0064 and uses 0060, 0061, 0062. Six
  plans are written in parallel with pre-assigned ranges; whoever lands
  second renumbers to the next free number at execution time.
  Migrations come in `.up.sql`/`.down.sql` pairs. Hand-written SQL, no
  ORM. `TestMigrateUpDown` (`internal/store/store_test.go`) exercises
  every down script.
- Secrets travel on stdin, never argv; never logged or echoed.
- Wiki pages live in `.gitbay/wiki/` (Parity, API, Admin, Threat-Model,
  CI, Users, Performance, and the `Architecture/` folder with its
  Known-Gaps table and controls matrix). Update the page in the same MR
  that changes the behaviour it describes, and remove the matching
  row from `Architecture/10-Known-Gaps.org`.
- Release notes go in `CHANGELOG.org` under the topmost heading that
  has no tag yet. If the top heading is a released version, add
  `* Unreleased` above it; whoever tags renames it.
- Writing style: plain, direct, no hype; code comments match the
  surrounding density. Comments and docs state facts, never
  before/after narration.
- Work in a worktree of `krz/gitbay`; the main checkout may hold
  another session's edits.

## Order and dependencies

| # | Branch | Closes | Migration | Depends on |
|---|---|---|---|---|
| 1 | `revoke-closes-connections` | #256 | none | — |
| 2 | `token-delegation` | #257 | 0060 | MR 1 (`Store.announce`, `fingerprint` e2e helper, `Exec` taking a key) |
| 3 | `key-expiry` | #277 | 0061 | MR 1 (sweep, per-exec check), MR 2 (`KeyOrigin`, `Ctx.Expires`) |
| 4 | `session-idle` | #276 | 0062 | — |
| 5 | `weblogin-limit` | #278 | none | — |

\#256 goes first. #257 and #277 both add columns to `ssh_keys`; both
are nullable `ADD COLUMN`s with no table rebuild, so they compose in
either order, and `KeyOrigin` (MR 2) is the one insert path both use.

Other plans:

- Plan 5 (web-ux) #264 depends on MR 2's `--scope read` default.
- Plan 3 (server-hardening) touches the same code: #262 limits
  concurrent git processes in `gitutil.Transport` / `sshd.runGit`
  (MR 1 changes both signatures), #275 audits refused commands in
  `control.Dispatch` (MR 2 adds a refusal there, which #275 should
  audit like the others), #282 changes `hookd`. Whoever lands second
  rebases; the conflicts are mechanical.

## File map

| File | MR | Responsibility |
|---|---|---|
| `internal/store/revoke.go` (create) | 1 | `Revoked`, `OnRevoke`, `announce`, `LiveSSHKeys` |
| `internal/store/store.go` | 1 | subscriber fields on `Store` |
| `internal/store/users.go` | 1, 2, 3 | removals announce; `KeyOrigin`, `AddSSHKeyFrom`; `expires_at` |
| `internal/sshd/sshd.go` | 1, 3 | connection tracking, `cut`, sweep, per-exec check, `Exec(key)`; expired keys refused |
| `internal/gitutil/gitutil.go`, `proc_unix.go`, `proc_other.go` | 1 | `Transport` takes a cancel channel, kills the process group |
| `cmd/gitbayd/system.go` | 1, 3 | `Exec` call; expired keys in system mode |
| `internal/control/control.go` | 2 | `MintsCredential`, `Ctx.TokenID`, `Ctx.Expires`, the refusal |
| `internal/store/tokens.go` | 2 | token id, creator, chained revoke |
| `internal/control/token.go` | 2, 3 | default read, creator, `revoke --created`; `ttlFlag` |
| `internal/control/identity.go`, `deploykey.go`, `runnerrepo.go`, `adminhost.go`, `register.go`, `web.go` | 2, 3, 4, 5 | flags, creator, `--ttl`, list columns, login limit |
| `internal/httpd/api.go` | 2 | token into `Ctx` |
| `internal/store/sessions.go` | 4 | sliding session expiry |
| `internal/control/loginlink.go` | 5 | comment |
| `e2e/revoke_test.go` (create) | 1 | multiplexed connection cut |
| `e2e/tokenorigin_test.go` (create) | 2 | expiring token refused, `revoke --created` |
| `.gitbay/wiki/*`, `CHANGELOG.org` | all | docs in the MR that changes behaviour |

---

# MR 1: removing a key closes its connections (branch `revoke-closes-connections`, #256)

Decision on the issue: revocation is immediate, running commands
included. What that means here, stated in the Threat-Model page:

- Every exec and every git transport session re-reads its key: gone,
  moved to another account, or re-scoped takes effect on the next
  command.
- `keys remove`, `repo deploy-key remove`, `admin user disable`,
  `admin user delete` and (MR 2) `token revoke --created` close every
  connection opened by an affected key. A git transport on it is
  killed with its process group. A control command in flight loses its
  channel; one that watches `Done` (`build log --follow`) stops, one
  already inside its store write finishes that write and its output is
  lost.
- A push cut before its pre-receive hook answers updates no refs. The
  ref transaction that follows the hook is not interrupted mid-write in
  practice; it is milliseconds long.
- Revocations committed by another process (`gitbayd admin` on the
  host) are found by a 15-second sweep.
- System mode (`ssh.mode = "system"`) runs one process per exec, so
  the per-exec check applies; a forced-command process already running
  is not cut (open question 4).

### Task 1.1: the store announces revocations and answers which keys are live

**Files:**
- Create: `internal/store/revoke.go`
- Modify: `internal/store/store.go:23-30` (`Store` struct)
- Modify: `internal/store/users.go` — `DeleteUser` (58-101), `SetUserDisabled` (171-194), `RemoveSSHKey` (291-309), `RemoveDeployKey` (534-554)
- Test: `internal/store/revoke_test.go` (create)

**Interfaces:**
- Produces:
  - `type Revoked struct { KeyIDs []int64; UserID int64 }`
  - `func (s *Store) OnRevoke(f func(Revoked))`
  - `func (s *Store) announce(r Revoked)` (package-private; MR 2 calls it)
  - `func (s *Store) LiveSSHKeys(ids []int64) (map[int64]bool, error)`

- [ ] **Step 1: Write the failing test**

`internal/store/revoke_test.go`:

```go
package store

import (
	"slices"
	"testing"
)

func revokeFixture(t *testing.T) (*Store, int64, *[]Revoked) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []Revoked
	s.OnRevoke(func(r Revoked) { got = append(got, r) })
	return s, uid, &got
}

func keyID(t *testing.T, s *Store, fp string) int64 {
	t.Helper()
	k, err := s.SSHKeyByFingerprint(fp)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID
}

func TestRemovalsAnnounceTheirKeys(t *testing.T) {
	s, uid, got := revokeFixture(t)
	if err := s.AddSSHKey(uid, "SHA256:a", "ssh-ed25519", []byte("a"), "full", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKey(uid, "SHA256:d", "ssh-ed25519", []byte("d"), "deploy:7:ro", ""); err != nil {
		t.Fatal(err)
	}
	a, d := keyID(t, s, "SHA256:a"), keyID(t, s, "SHA256:d")

	if err := s.RemoveSSHKey(uid, "SHA256:a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDeployKey(7, "SHA256:d"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(uid, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(uid, false); err != nil {
		t.Fatal(err)
	}
	want := []Revoked{{KeyIDs: []int64{a}}, {KeyIDs: []int64{d}}, {UserID: uid}}
	if !slices.EqualFunc(*got, want, func(x, y Revoked) bool {
		return slices.Equal(x.KeyIDs, y.KeyIDs) && x.UserID == y.UserID
	}) {
		t.Fatalf("announced %+v, want %+v (enabling announces nothing)", *got, want)
	}
	// A removal that found nothing announces nothing.
	if err := s.RemoveSSHKey(uid, "SHA256:a"); err != ErrNotFound {
		t.Fatalf("second remove: %v", err)
	}
	if len(*got) != 3 {
		t.Fatalf("a miss was announced: %+v", *got)
	}
}

func TestDeleteUserAnnounces(t *testing.T) {
	s, uid, got := revokeFixture(t)
	if err := s.DeleteUser(uid); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].UserID != uid {
		t.Fatalf("announced %+v", *got)
	}
}

func TestLiveSSHKeys(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct {
		uid int64
		fp  string
	}{{uid, "SHA256:a"}, {bob, "SHA256:b"}} {
		if err := s.AddSSHKey(k.uid, k.fp, "ssh-ed25519", []byte(k.fp), "full", ""); err != nil {
			t.Fatal(err)
		}
	}
	a, b := keyID(t, s, "SHA256:a"), keyID(t, s, "SHA256:b")
	if _, err := s.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", bob); err != nil {
		t.Fatal(err)
	}
	live, err := s.LiveSSHKeys([]int64{a, b, 999})
	if err != nil {
		t.Fatal(err)
	}
	if !live[a] || live[b] || live[999] {
		t.Fatalf("live = %v; want only %d", live, a)
	}
	if live, err := s.LiveSSHKeys(nil); err != nil || len(live) != 0 {
		t.Fatalf("no ids: %v %v", live, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run 'TestRemovalsAnnounceTheirKeys|TestDeleteUserAnnounces|TestLiveSSHKeys' -count=1`
Expected: FAIL to compile, `s.OnRevoke undefined`.

- [ ] **Step 3: Add the subscriber fields**

In `internal/store/store.go`, the `Store` struct becomes:

```go
type Store struct {
	DB *sql.DB

	// logWait holds one channel per build someone is following, closed
	// by the next change to that build's row (BuildLogWait).
	logMu   sync.Mutex
	logWait map[int64]chan struct{}

	// onRevoke runs after each key revocation this process commits.
	revokeMu sync.Mutex
	onRevoke []func(Revoked)
}
```

- [ ] **Step 4: Create `internal/store/revoke.go`**

```go
package store

import (
	"slices"
	"strings"
)

// Revoked names SSH keys that stopped being valid: by id, or every key
// of an account. The SSH listener closes the connections they opened.
type Revoked struct {
	KeyIDs []int64
	UserID int64 // every key of this account; 0 for none
}

// OnRevoke registers f to run after each revocation this process
// commits. Revocations committed by another process (gitbayd admin on
// the host) are not announced; the listener's sweep finds those.
func (s *Store) OnRevoke(f func(Revoked)) {
	s.revokeMu.Lock()
	defer s.revokeMu.Unlock()
	s.onRevoke = append(s.onRevoke, f)
}

// announce runs the subscribers. Call it after the commit, outside any
// transaction.
func (s *Store) announce(r Revoked) {
	s.revokeMu.Lock()
	fs := slices.Clone(s.onRevoke)
	s.revokeMu.Unlock()
	for _, f := range fs {
		f(r)
	}
}

// LiveSSHKeys reports which of ids still name a registered key on an
// account that is not disabled.
func (s *Store) LiveSSHKeys(ids []int64) (map[int64]bool, error) {
	live := map[int64]bool{}
	if len(ids) == 0 {
		return live, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB.Query(`SELECT k.id FROM ssh_keys k JOIN users u ON u.id = k.user_id
		WHERE u.disabled = 0 AND k.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}
```

- [ ] **Step 5: Announce from the four removals**

`RemoveSSHKey` in `internal/store/users.go`:

```go
// RemoveSSHKey removes a key owned by userID, bumps the key epoch, and
// announces the revocation.
func (s *Store) RemoveSSHKey(userID int64, fingerprint string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("DELETE FROM ssh_keys WHERE user_id = ? AND fingerprint = ? RETURNING id", userID, fingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.announce(Revoked{KeyIDs: []int64{id}})
	return nil
}
```

`RemoveDeployKey`:

```go
// RemoveDeployKey removes a deploy key from a repository by fingerprint;
// any repo admin may remove it regardless of who added it.
func (s *Store) RemoveDeployKey(repoID int64, fingerprint string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(
		"DELETE FROM ssh_keys WHERE fingerprint = ? AND scope LIKE 'deploy:' || ? || ':%' RETURNING id",
		fingerprint, repoID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.announce(Revoked{KeyIDs: []int64{id}})
	return nil
}
```

`SetUserDisabled`, the tail from `if disabled {`:

```go
	if disabled {
		// A pending login link is a session in waiting, so it goes with
		// the sessions and API tokens. Re-enabling means minting again.
		for _, table := range []string{"web_sessions", "api_tokens", "login_tokens"} {
			if _, err := s.DB.Exec("DELETE FROM "+table+" WHERE user_id = ?", userID); err != nil {
				return err
			}
		}
		s.announce(Revoked{UserID: userID})
	}
	return nil
}
```

Update its doc comment's last clause to: "and leaves the SSH keys registered but refused at every entry point until re-enabled; connections they opened are closed."

`DeleteUser`, the tail:

```go
	res, err := s.DB.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.announce(Revoked{UserID: id})
	return nil
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/store -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/revoke.go internal/store/revoke_test.go internal/store/store.go internal/store/users.go
git commit -S -m "store: announce key revocations; LiveSSHKeys

Ref #256"
```

### Task 1.2: `gitutil.Transport` can be cancelled

**Files:**
- Modify: `internal/gitutil/gitutil.go:39-56`
- Create: `internal/gitutil/proc_unix.go`, `internal/gitutil/proc_other.go`
- Test: `internal/gitutil/transport_test.go` (create)

**Interfaces:**
- Produces: `func Transport(service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string, maxPack int64, cancel <-chan struct{}) error` — closing `cancel` kills the git process and its children; a nil `cancel` never fires.

- [ ] **Step 1: Write the failing test**

`internal/gitutil/transport_test.go`:

```go
package gitutil

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Closing cancel kills the transport; it does not wait for the client
// to hang up. Stdin ends only after the kill, as a cut connection's
// does, so a clean exit here would mean the kill never happened.
func TestTransportCancelKillsGit(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	in, w := io.Pipe()
	cancel := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- Transport("git-upload-pack", dir, in, io.Discard, io.Discard, nil, 0, cancel) }()
	close(cancel)
	time.AfterFunc(500*time.Millisecond, func() { w.Close() })
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Fatalf("Transport returned %v, want the process killed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after cancel")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/gitutil -run TestTransportCancelKillsGit -count=1`
Expected: FAIL to compile, too many arguments to `Transport`.

- [ ] **Step 3: Process-group helpers**

`internal/gitutil/proc_unix.go`:

```go
//go:build unix

package gitutil

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in a process group of its own, so killTree
// ends what it started too: receive-pack runs index-pack and the hooks.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		cmd.Process.Kill()
	}
}
```

`internal/gitutil/proc_other.go`:

```go
//go:build !unix

package gitutil

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
```

- [ ] **Step 4: Transport**

Replace `Transport` in `internal/gitutil/gitutil.go`:

```go
// Transport runs a git transport service against repoPath with the
// client's streams. Closing cancel kills the service and everything it
// started; a push killed before its pre-receive hook answers updates no
// refs.
func Transport(service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string, maxPack int64, cancel <-chan struct{}) error {
	var args []string
	switch service {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
		if service == "git-receive-pack" && maxPack > 0 {
			args = []string{"-c", fmt.Sprintf("receive.maxInputSize=%d", maxPack)}
		}
		args = append(args, strings.TrimPrefix(service, "git-"), repoPath)
	default:
		return fmt.Errorf("unknown service %q", service)
	}
	cmd := exec.Command(toolpath.Look("git"), args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = errW
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-cancel:
			killTree(cmd)
		case <-finished:
		}
	}()
	err := cmd.Wait()
	close(finished)
	return err
}
```

If the current doc comment above `Transport` (line 38 and up) says something different, keep its first sentence and replace the rest with the above.

- [ ] **Step 5: Fix the caller so the tree builds**

In `internal/sshd/sshd.go:464`, pass `nil` for now (Task 1.3 wires the channel):

```go
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, env, maxPack, nil); err != nil {
```

- [ ] **Step 6: Run the tests**

Run: `go build ./... && go test ./internal/gitutil -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/gitutil internal/sshd/sshd.go
git commit -S -m "gitutil: Transport takes a cancel channel and kills its process group

Ref #256"
```

### Task 1.3: sshd re-reads the key per exec and cuts revoked connections

**Files:**
- Modify: `internal/sshd/sshd.go` — `conn` (46-53), `New` (55-71), `Serve` (158-180), `handleConn` (214-238), `handleSession` (240-292), `runExec` (299-313), `Exec` (339-385), `runGit` (387-468)
- Modify: `cmd/gitbayd/system.go:97`
- Modify: `internal/sshd/sshd_test.go:20-87` (`followServer` split)
- Test: `internal/sshd/revoke_test.go` (create)

**Interfaces:**
- Consumes: `store.Revoked`, `Store.OnRevoke`, `Store.LiveSSHKeys` (Task 1.1); `gitutil.Transport(..., cancel)` (Task 1.2).
- Produces:
  - `func Exec(cfg config.Config, st *store.Store, user store.User, key store.SSHKey, term control.Term, cmdline string, stdin io.Reader, stdout, stderr io.Writer, done, stopping, revoked <-chan struct{}) int` — scope and audit source come from `key`.
  - `func (s *Server) sweepOnce()` (tests call it).
  - Test helpers in package `sshd`: `type testServer struct{ srv *Server; st *store.Store; client *ssh.Client; uid, keyID int64; fp string }`, `newTestServer(t) testServer`, `withBuild(t, ts)`, `execStatus(client, cmd) (int, string)`, `waitClosed(t, client)`.

- [ ] **Step 1: Split the test fixture**

In `internal/sshd/sshd_test.go`, replace `followServer` (lines 20-87) with:

```go
// testServer is an embedded server over a fresh store holding alice
// with one full-scope key, and a client connected with that key.
type testServer struct {
	srv    *Server
	st     *store.Store
	client *ssh.Client
	uid    int64
	keyID  int64
	fp     string
}

func newTestServer(t *testing.T) testServer {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	fp := ssh.FingerprintSHA256(pub)
	if err := st.AddSSHKey(uid, fp, pub.Type(), pub.Marshal(), "full", "test"); err != nil {
		t.Fatal(err)
	}
	key, err := st.SSHKeyByFingerprint(fp)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.Root = root
	srv, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return testServer{srv: srv, st: st, client: client, uid: uid, keyID: key.ID, fp: fp}
}

// withBuild gives alice the public repo alice/app and a queued build 1
// whose log has one line.
func withBuild(t *testing.T, ts testServer) {
	t.Helper()
	repoID, err := ts.st.CreateRepo("user", ts.uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ts.st.CreateBuild(repoID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.st.AppendBuildLog(id, []byte("queued\n")); err != nil {
		t.Fatal(err)
	}
}

// followServer starts an embedded server holding alice, her public repo
// alice/app and a queued build 1 whose log has one line, and returns it
// with a client connected as alice.
func followServer(t *testing.T) (*Server, *ssh.Client) {
	t.Helper()
	ts := newTestServer(t)
	withBuild(t, ts)
	return ts.srv, ts.client
}
```

Run: `go test ./internal/sshd -count=1`
Expected: PASS (behaviour unchanged).

- [ ] **Step 2: Write the failing tests**

`internal/sshd/revoke_test.go`:

```go
package sshd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// execStatus runs cmd on a new session and returns its exit status and
// stderr; -1 when the session could not run.
func execStatus(client *ssh.Client, cmd string) (int, string) {
	sess, err := client.NewSession()
	if err != nil {
		return -1, err.Error()
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run(cmd)
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitStatus(), stderr.String()
	}
	return -1, err.Error()
}

// waitClosed fails unless the server closes the client's connection
// within five seconds.
func waitClosed(t *testing.T, client *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() { client.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open")
	}
}

// Each exec reads the key again. The rows change behind the store's
// back here, so no revocation is announced and the connection stays up:
// what refuses the command is the per-exec check alone.
func TestExecRevalidatesKey(t *testing.T) {
	ts := newTestServer(t)
	if code, errOut := execStatus(ts.client, "whoami"); code != 0 {
		t.Fatalf("whoami: %d %s", code, errOut)
	}
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET scope = 'git' WHERE id = ?", ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "does not allow control commands") {
		t.Fatalf("whoami after re-scope: %d %q", code, errOut)
	}
	if _, err := ts.st.DB.Exec("DELETE FROM ssh_keys WHERE id = ?", ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "no longer registered") {
		t.Fatalf("whoami after delete: %d %q", code, errOut)
	}
}

// Removing the key cuts the connection, ending a command running on it.
func TestRemoveKeyCutsConnection(t *testing.T) {
	ts := newTestServer(t)
	withBuild(t, ts)
	var stderr bytes.Buffer
	sess := startFollow(t, ts.client, &stderr)
	if err := ts.st.RemoveSSHKey(ts.uid, ts.fp); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("the follow exited cleanly after its key was removed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follow outlived its key")
	}
	waitClosed(t, ts.client)
}

func TestDisableCutsConnection(t *testing.T) {
	ts := newTestServer(t)
	if err := ts.st.SetUserDisabled(ts.uid, true); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, ts.client)
}

// A revocation made by another process is not announced here; the
// sweep finds it. A live key survives the sweep.
func TestSweepCutsOutOfProcessRevocation(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.sweepOnce()
	if code, errOut := execStatus(ts.client, "whoami"); code != 0 {
		t.Fatalf("the sweep cut a live key: %d %s", code, errOut)
	}
	if _, err := ts.st.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", ts.uid); err != nil {
		t.Fatal(err)
	}
	ts.srv.sweepOnce()
	waitClosed(t, ts.client)
}
```

- [ ] **Step 3: Run them and see them fail**

Run: `go test ./internal/sshd -run 'TestExecRevalidatesKey|TestRemoveKeyCutsConnection|TestDisableCutsConnection|TestSweepCutsOutOfProcessRevocation' -count=1`
Expected: FAIL to compile, `ts.srv.sweepOnce undefined`.

- [ ] **Step 4: Track the key on each connection**

In `internal/sshd/sshd.go` add `"slices"` to the imports. Replace `conn`:

```go
// conn is one accepted connection and how many sessions it is running.
// A CLI's shared connection sits idle between commands; on shutdown an
// idle connection is closed at once and only a session mid-command is
// waited for (#141).
type conn struct {
	net    net.Conn
	active atomic.Int32
	// keyID and userID are the key that authenticated the connection and
	// its account: 0 before the handshake and for an unregistered key.
	// Guarded by Server.mu.
	keyID, userID int64
	revoked       chan struct{} // closed by cut
	cutOnce       sync.Once
}

// cut ends the connection because its key was revoked: a git transport
// on it is killed, and every other command loses its channel.
func (c *conn) cut() {
	c.cutOnce.Do(func() { close(c.revoked) })
	c.net.Close()
}
```

In `New`, after `s.sshCfg = sc`:

```go
	st.OnRevoke(s.revoke)
```

`Serve` becomes:

```go
// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	served := make(chan struct{})
	defer close(served)
	go s.sweep(served)
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		c := &conn{net: nc, revoked: make(chan struct{})}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.sessions.Add(1)
		go func() {
			defer s.sessions.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
			s.handleConn(c)
		}()
	}
}
```

Add after `Serve`:

```go
// revoke closes the connections opened by the keys r names.
func (s *Server) revoke(r store.Revoked) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.keyID == 0 {
			continue
		}
		if (r.UserID != 0 && c.userID == r.UserID) || slices.Contains(r.KeyIDs, c.keyID) {
			c.cut()
		}
	}
}

// sweepInterval bounds how long a revocation this process was not told
// about (gitbayd admin on the host) leaves a connection open.
const sweepInterval = 15 * time.Second

func (s *Server) sweep(served <-chan struct{}) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sweepOnce()
		case <-served:
			return
		case <-s.stopping:
			return
		}
	}
}

// sweepOnce cuts every connection whose key is no longer live. Only
// connections whose key was asked about are judged: one that
// authenticated while the query ran waits for the next sweep.
func (s *Server) sweepOnce() {
	asked := map[int64]bool{}
	s.mu.Lock()
	for c := range s.conns {
		if c.keyID != 0 {
			asked[c.keyID] = true
		}
	}
	s.mu.Unlock()
	if len(asked) == 0 {
		return
	}
	live, err := s.st.LiveSSHKeys(slices.Collect(maps.Keys(asked)))
	if err != nil {
		slog.Error("ssh sweep: key lookup", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if asked[c.keyID] && !live[c.keyID] {
			c.cut()
		}
	}
}
```

Add `"maps"` to the imports.

In `handleConn`, after `defer sconn.Close()`:

```go
	ext := sconn.Permissions.Extensions
	s.mu.Lock()
	c.keyID, _ = strconv.ParseInt(ext["key-id"], 10, 64)
	c.userID, _ = strconv.ParseInt(ext["user-id"], 10, 64)
	s.mu.Unlock()
```

and pass `c` to the session: `s.handleSession(c, sconn, ch, chReqs)`.

`handleSession` takes the connection:

```go
func (s *Server) handleSession(c *conn, sconn *ssh.ServerConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
```

and its exec case calls `code := s.runExec(c, sconn, ch, term, payload.Command, done)`.

- [ ] **Step 5: Re-read the key per exec**

Replace `runExec`:

```go
func (s *Server) runExec(c *conn, sconn *ssh.ServerConn, ch ssh.Channel, term control.Term, cmdline string, done <-chan struct{}) int {
	ext := sconn.Permissions.Extensions
	if blob := ext["anon-key"]; blob != "" {
		return s.runAnonymous(ch, blob, cmdline)
	}
	userID, _ := strconv.ParseInt(ext["user-id"], 10, 64)
	keyID, _ := strconv.ParseInt(ext["key-id"], 10, 64)
	// A connection outlives its commands, so the key is read again for
	// each one: what it may do is what it may do now (#256).
	key, err := s.st.SSHKeyByID(keyID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && key.UserID != userID) {
		fmt.Fprintln(ch.Stderr(), "this key is no longer registered")
		return protocol.ExitDenied
	}
	if err != nil {
		slog.Error("ssh exec: key lookup", "err", err)
		fmt.Fprintln(ch.Stderr(), "authentication temporarily unavailable")
		return protocol.ExitFailure
	}
	user, err := s.st.UserByID(userID)
	if err != nil {
		fmt.Fprintln(ch.Stderr(), "account no longer exists")
		return protocol.ExitDenied
	}
	_ = s.st.TouchSSHKey(keyID)
	return Exec(s.cfg, s.st, user, key, term, cmdline, ch, ch, ch.Stderr(), done, s.stopping, c.revoked)
}
```

- [ ] **Step 6: `Exec` takes the key; `runGit` takes the cancel channel**

```go
// Exec runs one SSH exec command line for an authenticated key. It is the
// single dispatch path shared by the embedded listener and the system-sshd
// forced command (gitbayd shell). Closing revoked kills a git transport.
func Exec(cfg config.Config, st *store.Store, user store.User, key store.SSHKey, term control.Term, cmdline string,
	stdin io.Reader, stdout, stderr io.Writer, done, stopping, revoked <-chan struct{}) int {
```

Inside `Exec`: `runGit(cfg, st, user, key.Scope, argv, stdin, stdout, stderr, revoked)`, `runLFSAuthenticate(cfg, st, user, key.Scope, argv, stdout, stderr)`, and in the `Ctx` literal `Scope: key.Scope, Source: key.Fingerprint`.

`runGit` gains a last parameter `revoked <-chan struct{}` and passes it on:

```go
func runGit(cfg config.Config, st *store.Store, user store.User, scope string, argv []string,
	stdin io.Reader, stdout, stderr io.Writer, revoked <-chan struct{}) int {
```

```go
	if err := gitutil.Transport(service, dir, stdin, stdout, stderr, env, maxPack, revoked); err != nil {
```

In `cmd/gitbayd/system.go:97`:

```go
			code := sshd.Exec(cfg, st, user, key, control.ParseTerm(os.Getenv("GITBAY_TERM")), cmdline, os.Stdin, os.Stdout, os.Stderr, nil, nil, nil)
```

- [ ] **Step 7: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/sshd ./internal/gitutil ./internal/store -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/sshd cmd/gitbayd/system.go
git commit -S -m "sshd: re-read the key per exec; revocation cuts its connections

Ref #256"
```

### Task 1.4: e2e — a multiplexed connection is cut, a push in flight moves no ref

**Files:**
- Create: `e2e/revoke_test.go`

**Interfaces:**
- Produces (package `e2e`): `pkt(s string) string`, `readPkt(r *bufio.Reader) (string, error)`, `fingerprint(t *testing.T, pubPath string) string` — MR 2 uses `fingerprint`.

- [ ] **Step 1: Write the test**

```go
package e2e

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pkt frames one pkt-line.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// readPkt reads one pkt-line; a flush reads as "".
func readPkt(r *bufio.Reader) (string, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", err
	}
	size, err := strconv.ParseUint(string(n[:]), 16, 16)
	if err != nil {
		return "", err
	}
	if size == 0 {
		return "", nil
	}
	buf := make([]byte, size-4)
	_, err = io.ReadFull(r, buf)
	return string(buf), err
}

// fingerprint is the SHA256 fingerprint of a public key file.
func fingerprint(t *testing.T, pubPath string) string {
	t.Helper()
	out, err := exec.Command("ssh-keygen", "-lf", pubPath).Output()
	if err != nil {
		t.Fatalf("ssh-keygen -lf: %v", err)
	}
	return strings.Fields(string(out))[1]
}

// Removing a key cuts the connections it opened: every session
// multiplexed on a ControlMaster, and a push in flight, which moves no
// ref (#256).
func TestRemovedKeyCutsMultiplexedConnection(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := setupPublicRepo(t, inst, "alice/app")
	spare := inst.newKey(t, "spare")
	pub, err := os.ReadFile(spare + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "keys", "add"); code != 0 {
		t.Fatalf("keys add: %s", errOut)
	}

	// The control socket sits under the system temp dir: t.TempDir() on
	// macOS is long enough to pass the 104-byte socket path limit.
	cmDir, err := os.MkdirTemp("", "cm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(cmDir) })
	muxArgs := []string{
		"-p", fmt.Sprint(inst.port),
		"-i", aliceKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(inst.sshDir, "known_hosts"),
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(cmDir, "%C"),
		"-o", "ControlPersist=60",
	}
	mux := func(args ...string) *exec.Cmd {
		return exec.Command("ssh", append(append([]string{}, muxArgs...), args...)...)
	}
	t.Cleanup(func() { mux("-O", "exit", "git@127.0.0.1").Run() })

	if out, err := mux("git@127.0.0.1", "whoami").Output(); err != nil || strings.TrimSpace(string(out)) != "alice" {
		t.Fatalf("whoami over the master: %v %q", err, out)
	}

	// A push held open mid-pack: the ref update is sent, the pack is not.
	push := mux("git@127.0.0.1", "git-receive-pack", "alice/app")
	stdin, err := push.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := push.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := push.Start(); err != nil {
		t.Fatal(err)
	}
	adv := bufio.NewReader(stdout)
	first, err := readPkt(adv)
	if err != nil || len(first) < 40 {
		t.Fatalf("advertisement: %q %v", first, err)
	}
	oldSHA := first[:40]
	for {
		line, err := readPkt(adv)
		if err != nil {
			t.Fatalf("advertisement: %v", err)
		}
		if line == "" {
			break
		}
	}
	newSHA := strings.Repeat("1", 40)
	io.WriteString(stdin, pkt(oldSHA+" "+newSHA+" refs/heads/main\x00report-status\n")+"0000")
	// A pack header announcing one object, and no object.
	stdin.Write([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"))
	exited := make(chan error, 1)
	go func() {
		io.Copy(io.Discard, adv)
		exited <- push.Wait()
	}()

	if _, errOut, code := inst.ssh(t, spare, "", "keys", "remove", fingerprint(t, aliceKey+".pub")); code != 0 {
		t.Fatalf("keys remove: %s", errOut)
	}
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("the push exited cleanly after its key was removed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the push outlived its key")
	}

	// The master went with the connection; a new one authenticates
	// again, and the key is unknown.
	if out, err := mux("git@127.0.0.1", "whoami").CombinedOutput(); err == nil {
		t.Fatalf("whoami after removal succeeded: %s", out)
	}
	refs := mustGit(t, t.TempDir(), inst.gitEnv(spare), "ls-remote", inst.sshURL("alice/app"), "refs/heads/main")
	if !strings.HasPrefix(refs, oldSHA) {
		t.Fatalf("main moved: %s, want %s", refs, oldSHA)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./e2e -run TestRemovedKeyCutsMultiplexedConnection -count=1`
Expected: PASS. To see it fail, stash Task 1.3's `st.OnRevoke(s.revoke)` line: the push then hangs until the 10-second timeout.

- [ ] **Step 3: Commit**

```bash
git add e2e/revoke_test.go
git commit -S -m "e2e: removing a key cuts its multiplexed connection and a push in flight

Ref #256"
```

### Task 1.5: docs

**Files:**
- Modify: `.gitbay/wiki/Architecture/10-Known-Gaps.org`, `09-Controls.org:28`, `05-Identity-and-Access.org:17-18`, `08-Operations.org:88-89`, `.gitbay/wiki/Threat-Model.org` (Trust boundaries), `.gitbay/wiki/Users.org` (after the keys block, ~line 70)

- [ ] **Step 1: Edit the pages**

`10-Known-Gaps.org`: delete the `#256` row. In the paragraph under the table, drop "#256 closes a removed key's connections, running commands included;" so it opens "Decisions already taken on these: #257 refuses ...".

`09-Controls.org`, the revocation row becomes:

```
| Revocation takes effect immediately         | in place | removing a key or disabling an account closes its connections; every exec re-reads its key (=internal/sshd/sshd.go=) |
```

`05-Identity-and-Access.org`, the SSH user key and deploy key rows' Revocation cells become `=keys remove= (own keys); closes its connections` and `=repo deploy-key remove= (repo admin); closes its connections`.

`08-Operations.org`: delete the two lines "Open connections of a removed key keep working until they close; see #256."

`Threat-Model.org`, add to "Trust boundaries" after the "SSH public key = identity" bullet:

```
- *Revocation is immediate.* Every exec and every git transport session
  re-reads its key. Removing a key, removing a deploy key, disabling or
  deleting an account closes the connections the affected keys opened:
  a git transport is killed with its children, and a push killed before
  its pre-receive hook answers moves no ref. A control command already
  inside its database write finishes it; its output is lost. A
  revocation made by =gitbayd admin= on the host, another process, is
  found within 15 seconds. In =ssh.mode = "system"= each exec is its
  own process: the next exec is refused, one already running is not cut.
```

`Users.org`, after the paragraph that ends "Labels are one line of up to 64 bytes.":

```
Removing a key closes every connection it opened, including the CLI's
shared one; removing the key the current command runs on ends that
command's connection too.
```

- [ ] **Step 2: Commit, open the MR**

```bash
git add .gitbay/wiki
git commit -S -m "wiki: revocation closes open connections

Closes #256"
git push -u origin revoke-closes-connections
gitbay mr create --source revoke-closes-connections --target main --title "sshd: removing a key closes its connections"
```

Merge with `--strategy ff` once CI is green; delete the branch locally and on the remote.

---

# MR 2: expiring credentials cannot mint; credentials record their token (branch `token-delegation`, #257)

Decisions on the issue: a token with an expiry is refused on every
credential-minting command, marked on `Command` and checked in
`Dispatch`; tokens and keys record the token that created them;
`token revoke` lists what the token created and can revoke it too;
`token create` defaults to `--scope read`.

Commands marked `MintsCredential`: `token create`, `keys add`,
`repo deploy-key add`, `repo runner add` (attaches or creates a key
that can claim builds), `web login` (a login link opens a seven-day
session), `admin invite`, `admin user create` (with `--key` or a
verified address it is a way in), `email verify` and
`admin email verify` (a verified address receives login links). See
open question 2.

`created_by_token` references `api_tokens(id) ON DELETE SET NULL`: a
revoked token's id is never reused for a live row, because SQLite
reuses the highest rowid after it is deleted and a dangling integer
would then name the wrong token. Revoking without `--created` lists
what the token made and then drops the link.

### Task 2.1: migration 0060 and the store

**Files:**
- Create: `internal/store/migrations/0060_credential_origin.up.sql`, `.down.sql`
- Modify: `internal/store/tokens.go` (whole file)
- Modify: `internal/store/users.go` — `SSHKey` (18-28), `AddSSHKey` (270-289), `ListSSHKeys` (335-353)
- Test: `internal/store/tokens_test.go` (create)

**Interfaces:**
- Consumes: `Store.announce`, `Revoked` (MR 1).
- Produces:
  - `type APIToken struct { ID int64; Name, Scope, CreatedAt string; ExpiresAt, LastUsedAt *time.Time; CreatedBy string }` — `CreatedBy` is the creating token's name, "" for none.
  - `func (s *Store) CreateAPIToken(userID int64, name, tokenHash, scope string, expires *time.Time, createdByToken int64) error`
  - `func (s *Store) APITokenUser(tokenHash string) (User, APIToken, error)`
  - `type Created struct { Tokens []string; Keys []string }` — token names and key fingerprints.
  - `func (s *Store) RevokeAPIToken(userID int64, name string, withCreated bool) (Created, error)`
  - `type KeyOrigin struct { CreatedByToken int64 }` (MR 3 adds `ExpiresAt`)
  - `func (s *Store) AddSSHKeyFrom(userID int64, fingerprint, algo string, blob []byte, scope, label string, o KeyOrigin) error`; `AddSSHKey` keeps its signature and calls it with `KeyOrigin{}`.
  - `SSHKey.CreatedBy string` — filled by `ListSSHKeys` only.

- [ ] **Step 1: Write the failing test**

`internal/store/tokens_test.go`:

```go
package store

import (
	"slices"
	"testing"
	"time"
)

func tokenID(t *testing.T, s *Store, hash string) int64 {
	t.Helper()
	_, tok, err := s.APITokenUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	return tok.ID
}

// parent made child, child made grandchild and a key; the key belongs
// to another account, as admin user create --key makes one.
func tokenChain(t *testing.T) (*Store, int64, *[]Revoked) {
	t.Helper()
	s, uid, got := revokeFixture(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(uid, "parent", "h-parent", "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(uid, "child", "h-child", "full", nil, tokenID(t, s, "h-parent")); err != nil {
		t.Fatal(err)
	}
	child := tokenID(t, s, "h-child")
	if err := s.CreateAPIToken(uid, "grandchild", "h-grand", "read", nil, child); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKeyFrom(bob, "SHA256:k", "ssh-ed25519", []byte("k"), "full", "", KeyOrigin{CreatedByToken: child}); err != nil {
		t.Fatal(err)
	}
	return s, uid, got
}

func TestTokenRecordsItsCreator(t *testing.T) {
	s, uid, _ := tokenChain(t)
	toks, err := s.ListAPITokens(uid)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, tk := range toks {
		by[tk.Name] = tk.CreatedBy
	}
	if by["parent"] != "" || by["child"] != "parent" || by["grandchild"] != "child" {
		t.Fatalf("created by: %v", by)
	}
	bob, _ := s.UserByUsername("bob")
	keys, err := s.ListSSHKeys(bob.ID)
	if err != nil || len(keys) != 1 || keys[0].CreatedBy != "child" {
		t.Fatalf("key created by: %+v %v", keys, err)
	}
}

func TestRevokeAPITokenListsWhatItCreated(t *testing.T) {
	s, uid, got := tokenChain(t)
	c, err := s.RevokeAPIToken(uid, "parent", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Tokens, []string{"child", "grandchild"}) || !slices.Equal(c.Keys, []string{"SHA256:k"}) {
		t.Fatalf("created = %+v", c)
	}
	// Listed, not removed; the link to the revoked parent is gone.
	toks, _ := s.ListAPITokens(uid)
	if len(toks) != 2 || toks[0].Name != "child" || toks[0].CreatedBy != "" {
		t.Fatalf("tokens after revoke: %+v", toks)
	}
	if _, err := s.SSHKeyByFingerprint("SHA256:k"); err != nil {
		t.Fatalf("the key went: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("announced %+v with nothing revoked but the token", *got)
	}
}

func TestRevokeAPITokenWithCreated(t *testing.T) {
	s, uid, got := tokenChain(t)
	k, _ := s.SSHKeyByFingerprint("SHA256:k")
	if _, err := s.RevokeAPIToken(uid, "parent", true); err != nil {
		t.Fatal(err)
	}
	if toks, _ := s.ListAPITokens(uid); len(toks) != 0 {
		t.Fatalf("tokens left: %+v", toks)
	}
	if _, err := s.SSHKeyByFingerprint("SHA256:k"); err != ErrNotFound {
		t.Fatalf("key left: %v", err)
	}
	if len(*got) != 1 || !slices.Equal((*got)[0].KeyIDs, []int64{k.ID}) {
		t.Fatalf("announced %+v", *got)
	}
	if _, err := s.RevokeAPIToken(uid, "parent", true); err != ErrNotFound {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestAPITokenUserCarriesExpiry(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	exp := time.Now().Add(time.Hour)
	if err := s.CreateAPIToken(uid, "brief", "h-brief", "full", &exp, 0); err != nil {
		t.Fatal(err)
	}
	_, tok, err := s.APITokenUser("h-brief")
	if err != nil || tok.ExpiresAt == nil || tok.Name != "brief" || tok.ID == 0 {
		t.Fatalf("token %+v %v", tok, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run 'TestTokenRecordsItsCreator|TestRevokeAPIToken|TestAPITokenUserCarriesExpiry' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Migration**

`internal/store/migrations/0060_credential_origin.up.sql`:

```sql
-- The API token a credential was created through. NULL when it was not,
-- and once that token is revoked.
ALTER TABLE api_tokens ADD COLUMN created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL;
ALTER TABLE ssh_keys ADD COLUMN created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL;
```

`internal/store/migrations/0060_credential_origin.down.sql`:

```sql
ALTER TABLE ssh_keys DROP COLUMN created_by_token;
ALTER TABLE api_tokens DROP COLUMN created_by_token;
```

- [ ] **Step 4: Tokens in the store**

Replace `internal/store/tokens.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type APIToken struct {
	ID         int64
	Name       string
	Scope      string
	CreatedAt  string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedBy  string // name of the token that created this one; "" for none
}

// nullID stores 0 as NULL.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateAPIToken stores a token hash; expires nil means no expiry,
// createdByToken 0 means it was not created through a token.
func (s *Store) CreateAPIToken(userID int64, name, tokenHash, scope string, expires *time.Time, createdByToken int64) error {
	var exp any
	if expires != nil {
		exp = fmtTime(*expires)
	}
	_, err := s.DB.Exec(
		"INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at, created_by_token) VALUES (?, ?, ?, ?, ?, ?)",
		userID, name, tokenHash, scope, exp, nullID(createdByToken))
	if isUniqueErr(err) {
		return fmt.Errorf("you already have a token named %q", name)
	}
	return err
}

// APITokenUser resolves a presented token to its user and the token;
// expired and unknown tokens fail identically.
func (s *Store) APITokenUser(tokenHash string) (User, APIToken, error) {
	var userID int64
	var t APIToken
	var exp sql.NullString
	err := s.DB.QueryRow(`
		SELECT user_id, id, name, scope, expires_at FROM api_tokens
		WHERE token_hash = ? AND (expires_at IS NULL OR expires_at > ?)`,
		tokenHash, fmtTime(time.Now())).Scan(&userID, &t.ID, &t.Name, &t.Scope, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, APIToken{}, ErrNotFound
	}
	if err != nil {
		return User{}, APIToken{}, err
	}
	t.ExpiresAt = parseTime(exp)
	s.DB.Exec("UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_hash = ?", tokenHash)
	u, err := s.UserByID(userID)
	return u, t, err
}

func (s *Store) ListAPITokens(userID int64) ([]APIToken, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.name, t.scope, t.created_at, t.expires_at, t.last_used_at, COALESCE(p.name, '')
		FROM api_tokens t LEFT JOIN api_tokens p ON p.id = t.created_by_token
		WHERE t.user_id = ? ORDER BY t.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var exp, used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &exp, &used, &t.CreatedBy); err != nil {
			return nil, err
		}
		t.ExpiresAt = parseTime(exp)
		t.LastUsedAt = parseTime(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Created is what a token made, directly or through tokens it made:
// token names and SSH key fingerprints.
type Created struct {
	Tokens []string `json:"tokens"`
	Keys   []string `json:"keys"`
}

// chainCTE selects the token named by the first argument and every
// token created from it, at any depth.
const chainCTE = `WITH RECURSIVE chain(id) AS (
	SELECT ? UNION SELECT t.id FROM api_tokens t JOIN chain ON t.created_by_token = chain.id)`

// RevokeAPIToken deletes the user's token by name and returns what it
// created. withCreated deletes those too; otherwise they stay and lose
// the link to the revoked token.
func (s *Store) RevokeAPIToken(userID int64, name string, withCreated bool) (Created, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return Created{}, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT id FROM api_tokens WHERE user_id = ? AND name = ?", userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Created{}, ErrNotFound
	}
	if err != nil {
		return Created{}, err
	}
	var c Created
	rows, err := tx.Query(chainCTE+` SELECT name FROM api_tokens WHERE id IN (SELECT id FROM chain) AND id != ? ORDER BY name`, id, id)
	if err != nil {
		return Created{}, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return Created{}, err
		}
		c.Tokens = append(c.Tokens, n)
	}
	rows.Close()
	var keyIDs []int64
	rows, err = tx.Query(chainCTE+` SELECT id, fingerprint FROM ssh_keys WHERE created_by_token IN (SELECT id FROM chain) ORDER BY id`, id)
	if err != nil {
		return Created{}, err
	}
	for rows.Next() {
		var kid int64
		var fp string
		if err := rows.Scan(&kid, &fp); err != nil {
			rows.Close()
			return Created{}, err
		}
		keyIDs = append(keyIDs, kid)
		c.Keys = append(c.Keys, fp)
	}
	rows.Close()

	if !withCreated {
		if _, err := tx.Exec("DELETE FROM api_tokens WHERE id = ?", id); err != nil {
			return Created{}, err
		}
		return c, tx.Commit()
	}
	if len(keyIDs) > 0 {
		args := make([]any, len(keyIDs))
		for i, k := range keyIDs {
			args[i] = k
		}
		if _, err := tx.Exec("DELETE FROM ssh_keys WHERE id IN (?"+strings.Repeat(", ?", len(keyIDs)-1)+")", args...); err != nil {
			return Created{}, err
		}
		if err := bumpKeyEpoch(tx); err != nil {
			return Created{}, err
		}
	}
	if _, err := tx.Exec(chainCTE+` DELETE FROM api_tokens WHERE id IN (SELECT id FROM chain)`, id); err != nil {
		return Created{}, err
	}
	if err := tx.Commit(); err != nil {
		return Created{}, err
	}
	if len(keyIDs) > 0 {
		s.announce(Revoked{KeyIDs: keyIDs})
	}
	return c, nil
}
```

Before writing `nullID`, run `grep -rn "func nullID" internal/store`; if one exists, use it and drop this copy.

- [ ] **Step 5: Keys in the store**

In `internal/store/users.go`, add to `SSHKey` after `LastUsedAt`:

```go
	CreatedBy   string // name of the API token that added the key; "" for none. ListSSHKeys only.
```

Replace `AddSSHKey`:

```go
// KeyOrigin is how a key came to be.
type KeyOrigin struct {
	CreatedByToken int64 // the API token that added it; 0 for none
}

// AddSSHKey registers a key and bumps the key epoch in one transaction.
func (s *Store) AddSSHKey(userID int64, fingerprint, algo string, blob []byte, scope, label string) error {
	return s.AddSSHKeyFrom(userID, fingerprint, algo, blob, scope, label, KeyOrigin{})
}

// AddSSHKeyFrom is AddSSHKey recording where the key came from.
func (s *Store) AddSSHKeyFrom(userID int64, fingerprint, algo string, blob []byte, scope, label string, o KeyOrigin) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope, label, created_by_token) VALUES (?, ?, ?, ?, ?, ?, ?)",
		userID, fingerprint, algo, blob, scope, label, nullID(o.CreatedByToken)); err != nil {
		if isUniqueErr(err) {
			return ErrDuplicateKey
		}
		return err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}
```

`ListSSHKeys`:

```go
func (s *Store) ListSSHKeys(userID int64) ([]SSHKey, error) {
	rows, err := s.DB.Query(
		`SELECT k.id, k.user_id, k.fingerprint, k.algo, k.blob, k.scope, k.label, k.created_at,
		        COALESCE(k.last_used_at, ''), COALESCE(t.name, '')
		 FROM ssh_keys k LEFT JOIN api_tokens t ON t.id = k.created_by_token
		 WHERE k.user_id = ? ORDER BY k.id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope, &k.Label, &k.CreatedAt, &k.LastUsedAt, &k.CreatedBy); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
```

- [ ] **Step 6: Run the store tests**

Run: `go test ./internal/store -count=1`
Expected: PASS. (`go build ./...` fails until Task 2.2 updates the callers.)

- [ ] **Step 7: Commit**

```bash
git add internal/store
git commit -S -m "store: tokens and keys record the token that created them; chained revoke

Ref #257"
```

### Task 2.2: `MintsCredential`, `Ctx.Expires`, and the API wiring

**Files:**
- Modify: `internal/control/control.go` — `Ctx` (21-57), `Command` (79-91), `Dispatch` (after line 161)
- Modify: `internal/httpd/api.go:30-78`, `:126-144`; `internal/httpd/apiread.go:26`
- Modify: registrations in `internal/control/token.go:16`, `identity.go:33`, `deploykey.go:16`, `runnerrepo.go:21`, `web.go:16`, `adminhost.go:24`, `:53`, `:58`, `register.go:38`
- Test: `internal/control/token_test.go` (create)

**Interfaces:**
- Consumes: `store.APITokenUser` returning `APIToken` (Task 2.1).
- Produces:
  - `Command.MintsCredential bool`
  - `Ctx.TokenID int64` — the API token behind the request, 0 for none.
  - `Ctx.Expires *time.Time` — when the credential behind the request lapses; nil when it does not. MR 3 sets it for keys.

- [ ] **Step 1: Write the failing tests**

`internal/control/token_test.go`:

```go
package control

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// The minting commands, pinned: adding one to the list, or dropping
// one, is a decision this test makes someone take.
func TestMintingCommandsMarked(t *testing.T) {
	want := []string{
		"admin email verify", "admin invite", "admin user create", "email verify",
		"keys add", "repo deploy-key add", "repo runner add", "token create", "web login",
	}
	var got []string
	for _, cmd := range Commands() {
		if cmd.MintsCredential {
			got = append(got, joinPath(cmd.Path))
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("MintsCredential on %q, want %q", got, want)
	}
}

// Dispatch refuses before the command runs, so no arguments are needed.
func TestExpiringCredentialCannotMint(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	for _, cmd := range Commands() {
		if !cmd.MintsCredential {
			continue
		}
		var out, errOut bytes.Buffer
		c := &Ctx{User: store.User{ID: 1, Username: "root", IsAdmin: true}, Scope: "full", Expires: &exp, Stdout: &out, Stderr: &errOut}
		if code := Dispatch(c, cmd.Path); code != protocol.ExitDenied || !strings.Contains(errOut.String(), "expires") {
			t.Errorf("%s: exit %d %q, want %d and the reason", joinPath(cmd.Path), code, errOut.String(), protocol.ExitDenied)
		}
	}
}

func TestTokenCreateDefaultsToReadAndRecordsCreator(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	if err := st.CreateAPIToken(uid, "parent", "h-parent", "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	_, parent, err := st.APITokenUser("h-parent")
	if err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Cfg.Limits.WriteRate = -1
	c.TokenID = parent.ID
	if code := Dispatch(c, []string{"token", "create", "--name", "child"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	toks, err := st.ListAPITokens(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range toks {
		if tk.Name == "child" && (tk.Scope != "read" || tk.CreatedBy != "parent") {
			t.Fatalf("child: %+v", tk)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run 'TestMintingCommandsMarked|TestExpiringCredentialCannotMint|TestTokenCreateDefaultsToRead' -count=1`
Expected: FAIL to compile, `unknown field MintsCredential`.

- [ ] **Step 3: `Ctx`, `Command`, `Dispatch`**

In `Ctx`, after `Source string`:

```go
	// TokenID is the API token behind this request, 0 for none. A
	// credential the request creates records it.
	TokenID int64
	// Expires is when the credential behind this request lapses; nil
	// when it does not. Dispatch refuses MintsCredential commands when
	// it is set.
	Expires *time.Time
```

In `Command`, after `ReadOnly`:

```go
	// MintsCredential marks a command that creates a credential or a way
	// to obtain one: tokens, keys, login links, invites, accounts,
	// verified addresses. An expiring credential may not run it.
	MintsCredential bool
```

In `Dispatch`, after the `c.ReadOnly && !cmd.ReadOnly` check:

```go
	// What an expiring credential creates would outlive it (#257).
	if cmd.MintsCredential && c.Expires != nil {
		return c.fail(protocol.ExitDenied,
			"%s creates a credential, and the one this request came with expires; use a token or key without an expiry", joinPath(cmd.Path))
	}
```

- [ ] **Step 4: Mark the nine commands**

Add `MintsCredential: true,` to each registration: `token create` (`token.go:16`), `keys add` (`identity.go:33`), `repo deploy-key add` (`deploykey.go:16`), `repo runner add` (`runnerrepo.go:21`), `web login` (`web.go:16`), `admin user create` (`adminhost.go:24`), `admin email verify` (`adminhost.go:53`), `admin invite` (`adminhost.go:58`), `email verify` (`register.go:38`). For example:

```go
	register(Command{Path: []string{"web", "login"},
		Summary:         "mint a one-time browser login URL",
		Usage:           "web login",
		MintsCredential: true,
		Examples:        []string{"web login"}, Run: runWebLogin})
```

- [ ] **Step 5: The API passes the token**

In `internal/httpd/api.go`, `apiAuth` returns the token:

```go
// apiAuth resolves the bearer token; failures are uniform 401s.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) (store.User, store.APIToken, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gitbay api"`)
		apiError(w, http.StatusUnauthorized, "missing bearer token; mint one over SSH: token create --name <n>")
		return store.User{}, store.APIToken{}, false
	}
	user, tok, err := s.st.APITokenUser(store.HashToken(strings.TrimSpace(token)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusUnauthorized, "invalid or expired token")
			return store.User{}, store.APIToken{}, false
		}
		apiError(w, http.StatusInternalServerError, "internal error")
		return store.User{}, store.APIToken{}, false
	}
	return user, tok, true
}
```

In `apiCmd`: `user, tok, ok := s.apiAuth(w, r)`, and in the `Ctx` literal replace `ReadOnly: scope == "read",` with:

```go
		ReadOnly: tok.Scope == "read",
		TokenID:  tok.ID,
		Expires:  tok.ExpiresAt,
```

`apiRead` keeps `user, _, ok := s.apiAuth(w, r)`; it compiles unchanged.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/control -run 'TestMintingCommandsMarked|TestExpiringCredentialCannotMint' -count=1`
Expected: PASS. `TestTokenCreateDefaultsToRead...` still fails until Task 2.3.

- [ ] **Step 7: Commit**

```bash
git add internal/control/control.go internal/control/token_test.go internal/control/*.go internal/httpd/api.go
git commit -S -m "control: expiring credentials cannot run credential-minting commands

Ref #257"
```

### Task 2.3: commands record their token; `token create` defaults to read; `token revoke --created`

**Files:**
- Modify: `internal/control/token.go` (registrations 16-34, `runTokenCreate` 49-88, `runTokenList` 90-122, `runTokenRevoke` 124-137)
- Modify: `internal/control/identity.go` — `runKeysList` (76-100), `runKeysAdd` (152)
- Modify: `internal/control/deploykey.go:71`, `internal/control/runnerrepo.go:60`, `internal/control/adminhost.go:125`
- Modify: `e2e/api_test.go:50`, `:266-282` (`mintToken`)

**Interfaces:**
- Consumes: `Ctx.TokenID`, `store.KeyOrigin`, `Store.AddSSHKeyFrom`, `Store.RevokeAPIToken` (Tasks 2.1–2.2).

- [ ] **Step 1: `token create`**

Registration:

```go
	register(Command{Path: []string{"token", "create"},
		Summary: "mint an API token (shown once)",
		Usage:   "token create --name <n> [--scope read|full] [--ttl 30d|720h]",
		Flags: []Flag{
			{"--name", "<n>", "the token's name", ""},
			{"--scope", "read|full", "what the token may do; full is needed to change anything", "read"},
			{"--ttl", "30d|720h", "how long the token is valid; an expiring token cannot mint credentials", "never expires"},
		},
		Examples:        []string{"token create --name laptop --ttl 30d", "token create --name phone --scope full"},
		MintsCredential: true,
		Run:             runTokenCreate})
```

In `runTokenCreate`: the `parseFlags` usage becomes `Usage: c.Cmd.Usage`; `name, scope, ttl := f.Value("--name"), "read", f.Value("--ttl")`; the store call:

```go
	if err := c.Store.CreateAPIToken(c.User.ID, name, store.HashToken(token), scope, expires, c.TokenID); err != nil {
```

- [ ] **Step 2: `token list` shows the creator in JSON**

In `runTokenList`, the `out` struct gains `CreatedBy string `json:"created_by,omitempty"`` after `LastUsedAt`, and the append becomes `out{t.Name, t.Scope, t.CreatedAt, t.ExpiresAt, t.LastUsedAt, t.CreatedBy}`. Plain output is unchanged.

- [ ] **Step 3: `token revoke [--created]`**

Registration:

```go
	register(Command{Path: []string{"token", "revoke"},
		Summary: "revoke an API token by name",
		Usage:   "token revoke <name> [--created]",
		Flags: []Flag{
			{"--created", "", "also revoke the tokens and keys it created, at any depth", ""},
		},
		Examples: []string{"token revoke laptop", "token revoke laptop --created"},
		Run:      runTokenRevoke})
```

```go
func runTokenRevoke(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Bools: []string{"--created"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	name := f.pos(0)
	if name == "" {
		return c.usage()
	}
	withCreated := f.Has("--created")
	created, err := c.Store.RevokeAPIToken(c.User.ID, name, withCreated)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no token named %q", name)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Revoked        string        `json:"revoked"`
		Created        store.Created `json:"created"`
		CreatedRevoked bool          `json:"created_revoked"`
	}
	d := out{name, created, withCreated}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "revoked %s\n", name)
		if len(created.Tokens)+len(created.Keys) == 0 {
			return
		}
		if withCreated {
			fmt.Fprintln(w, "and what it created:")
		} else {
			fmt.Fprintln(w, "it created these, still in place:")
		}
		for _, n := range created.Tokens {
			fmt.Fprintf(w, "  token %s\n", n)
		}
		for _, fp := range created.Keys {
			fmt.Fprintf(w, "  key %s\n", fp)
		}
	})
}
```

- [ ] **Step 4: Key-adding commands record the token**

`identity.go`, `runKeysAdd`:

```go
	if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label, store.KeyOrigin{CreatedByToken: c.TokenID}); err != nil {
```

`deploykey.go:71`:

```go
	if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label, store.KeyOrigin{CreatedByToken: c.TokenID}); err != nil {
```

`runnerrepo.go:60`:

```go
		if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), "runner", label, store.KeyOrigin{CreatedByToken: c.TokenID}); err != nil {
```

`adminhost.go:125`:

```go
		if err := c.Store.AddSSHKeyFrom(uid, fp, pub.Type(), pub.Marshal(), "full", label, store.KeyOrigin{CreatedByToken: c.TokenID}); err != nil {
```

`keys list` JSON: in `runKeysList` the `out` struct gains `CreatedBy string `json:"created_by,omitempty"`` and the append passes `k.CreatedBy`. Plain output is unchanged in this MR.

- [ ] **Step 5: e2e callers that write with a default-scope token**

`e2e/api_test.go:50`: `"token", "create", "--name", "ci", "--scope", "full", "--json"`.
`mintToken` (`e2e/api_test.go:268`): `"token", "create", "--name", name, "--scope", "full", "--json"`.
Then `grep -rn '"token", "create"' e2e` and check each remaining call: a token only used for reads, or for a refusal, needs nothing.

- [ ] **Step 6: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/control ./internal/store ./internal/httpd -count=1`
Expected: PASS, including `TestHelpIsComplete` (every flag in the usage is described) and `TestTokenCreateDefaultsToReadAndRecordsCreator`.

- [ ] **Step 7: Commit**

```bash
git add internal/control e2e/api_test.go
git commit -S -m "token: default --scope read; record the creating token; revoke --created

Ref #257"
```

### Task 2.4: e2e — delegation over the API

**Files:**
- Create: `e2e/tokenorigin_test.go`

**Interfaces:**
- Consumes: `fingerprint` (MR 1, `e2e/revoke_test.go`), `inst.apiCall`.

- [ ] **Step 1: Write the test**

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// An expiring token cannot mint a credential that outlives it, and
// revoking a token can take what it created with it (#257).
func TestTokenDelegation(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[api]\nenabled = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	mint := func(args ...string) (token, scope string) {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", append([]string{"token", "create", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("token create %v: %s", args, errOut)
		}
		var env struct {
			Data struct {
				Token string `json:"token"`
				Scope string `json:"scope"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("token create output: %v %s", err, out)
		}
		return env.Data.Token, env.Data.Scope
	}
	if _, scope := mint("--name", "plain"); scope != "read" {
		t.Fatalf("default scope %q, want read", scope)
	}
	brief, _ := mint("--name", "brief", "--scope", "full", "--ttl", "1h")
	lasting, _ := mint("--name", "lasting", "--scope", "full")

	spare := inst.newKey(t, "spare")
	pub, err := os.ReadFile(spare + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	status, body := inst.apiCall(t, brief, []string{"keys", "add"}, string(pub))
	if status != 403 || !strings.Contains(fmt.Sprint(body["error"]), "expires") {
		t.Fatalf("expiring token added a key: %d %v", status, body)
	}
	if status, _ := inst.apiCall(t, brief, []string{"whoami"}, ""); status != 200 {
		t.Fatalf("expiring token refused a read: %d", status)
	}
	if status, body := inst.apiCall(t, lasting, []string{"keys", "add"}, string(pub)); status != 200 {
		t.Fatalf("keys add: %d %v", status, body)
	}
	if status, body := inst.apiCall(t, lasting, []string{"token", "create", "--name", "child"}, ""); status != 200 {
		t.Fatalf("token create: %d %v", status, body)
	}
	if _, errOut, code := inst.ssh(t, spare, "", "whoami"); code != 0 {
		t.Fatalf("the added key does not work: %s", errOut)
	}

	out, errOut, code := inst.ssh(t, aliceKey, "", "token", "revoke", "lasting", "--created")
	if code != 0 || !strings.Contains(out, "token child") || !strings.Contains(out, fingerprint(t, spare+".pub")) {
		t.Fatalf("revoke --created: exit %d\n%s%s", code, out, errOut)
	}
	if _, _, code := inst.ssh(t, spare, "", "whoami"); code == 0 {
		t.Fatal("a key the revoked token created still works")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "token", "list"); strings.Contains(out, "child") {
		t.Fatalf("the child token survived:\n%s", out)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./e2e -run TestTokenDelegation -count=1`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add e2e/tokenorigin_test.go
git commit -S -m "e2e: expiring tokens refused on minting; revoke --created

Ref #257"
```

### Task 2.5: docs and release note

**Files:**
- Modify: `.gitbay/wiki/API.org:14-33` (Tokens), `.gitbay/wiki/Threat-Model.org` (Trust boundaries), `.gitbay/wiki/Architecture/05-Identity-and-Access.org:20`, `09-Controls.org:29`, `10-Known-Gaps.org`, `.gitbay/wiki/Parity.org:358`, `CHANGELOG.org`, `internal/web/templates/account.html:194`

- [ ] **Step 1: Edit the pages**

`API.org`, the Tokens section's first paragraph and code block become:

```
Tokens are minted wherever the registry is reached: over SSH, on the
API, anywhere. =token create= makes a =read= token unless =--scope full=
is given; a read token runs only commands marked read-only. A full-scope
token can mint another, but a token with a =--ttl= cannot run any
command that creates a credential — =token create=, =keys add=,
=repo deploy-key add=, =repo runner add=, =web login=, =admin invite=,
=admin user create=, =email verify=, =admin email verify= — since what
it made would outlive it. Give a token the narrowest scope and shortest
TTL that does its job, and revoke it when the job is over.

#+begin_src sh
gitbay auth token create --name ci [--scope read|full] [--ttl 30d]
gitbay auth token list
gitbay auth token revoke ci [--created]
#+end_src

Tokens and keys record the token they were created through. =token
revoke= prints what the token created, at any depth; with =--created=
it revokes those too, and their SSH connections close. Without it they
stay and the link is dropped.
```

`Threat-Model.org`, in Trust boundaries, replace "so a bearer token is worth exactly its scope and no more." with "so a bearer token is worth exactly its scope and no more, and a credential with an expiry cannot create one that outlives it."

`05-Identity-and-Access.org`: the API token row's Scope cell becomes `=read= (default) or =full=; with an expiry, no credential-minting command`, and its Revocation cell `=token revoke [--created]=`.

`09-Controls.org`:

```
| Delegation bounded by the delegating credential | in place | expiring tokens refused on =MintsCredential= commands; credentials record their creating token (=internal/control/control.go=) |
```

`10-Known-Gaps.org`: delete the `#257` row and the "#257 refuses credential creation ..." sentence, leaving the paragraph out if nothing remains in it.

`Parity.org`, after the `API token mint` row:

```
| API token revoke with what it created | yes | no  | no  |
```

`account.html:194`: `gitbay auth token create --name laptop # API tokens, read-only unless --scope full`.

`CHANGELOG.org`, under the unreleased heading (see Global constraints):

```
*Upgrade note.* =token create= makes a =read= token unless given
=--scope full=. A script that mints a token and then writes with it
must add =--scope full=. Existing tokens keep their scope.

- A token with a =--ttl= is refused on every command that creates a
  credential: tokens, keys, deploy keys, runner keys, login links,
  invites, accounts and verified addresses (#257).
- Tokens and SSH keys record the token they were created through.
  =token revoke <name>= lists what it created; =--created= revokes
  those too.
- Removing an SSH key, a deploy key, or disabling an account closes the
  connections the key opened, a push in flight included (#256).
```

(The #256 line belongs to MR 1's changes; add it here if MR 1 did not touch the changelog.)

- [ ] **Step 2: Commit, open the MR**

```bash
git add .gitbay/wiki CHANGELOG.org internal/web/templates/account.html
git commit -S -m "wiki: token delegation, read default; release note

Closes #257"
git push -u origin token-delegation
gitbay mr create --source token-delegation --target main --title "token: expiring tokens cannot mint credentials; record creator; default read scope"
```

---

# MR 3: optional expiry for SSH and deploy keys (branch `key-expiry`, #277)

The issue says to consider this with #257. An expiring key is treated
like an expiring token: `Exec` sets `Ctx.Expires`, so `Dispatch` refuses
the minting commands to it (open question 1).

### Task 3.1: migration 0061 and the store

**Files:**
- Create: `internal/store/migrations/0061_ssh_key_expiry.up.sql`, `.down.sql`
- Modify: `internal/store/users.go` — `SSHKey`, `KeyOrigin`, `AddSSHKeyFrom`, `SSHKeyByFingerprint` (324-333), `SSHKeyByID` (502-511), `ListSSHKeys`, `ListDeployKeys` (514-532)
- Modify: `internal/store/revoke.go` — `LiveSSHKeys`
- Test: `internal/store/keyexpiry_test.go` (create)

**Interfaces:**
- Produces:
  - `SSHKey.ExpiresAt *time.Time` — nil when the key never expires; filled by every key query.
  - `func (k SSHKey) Expired(now time.Time) bool`
  - `KeyOrigin.ExpiresAt *time.Time`
  - `LiveSSHKeys` also excludes expired keys.

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"testing"
	"time"
)

func TestKeyExpiry(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for fp, exp := range map[string]*time.Time{"SHA256:old": &past, "SHA256:new": &future, "SHA256:ever": nil} {
		if err := s.AddSSHKeyFrom(uid, fp, "ssh-ed25519", []byte(fp), "full", "", KeyOrigin{ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ids := map[string]int64{}
	for _, fp := range []string{"SHA256:old", "SHA256:new", "SHA256:ever"} {
		k, err := s.SSHKeyByFingerprint(fp)
		if err != nil {
			t.Fatal(err)
		}
		ids[fp] = k.ID
		byID, err := s.SSHKeyByID(k.ID)
		if err != nil || (byID.ExpiresAt == nil) != (k.ExpiresAt == nil) {
			t.Fatalf("%s by id: %+v %v", fp, byID, err)
		}
		if got, want := k.Expired(now), fp == "SHA256:old"; got != want {
			t.Errorf("%s Expired = %v, want %v", fp, got, want)
		}
	}
	live, err := s.LiveSSHKeys([]int64{ids["SHA256:old"], ids["SHA256:new"], ids["SHA256:ever"]})
	if err != nil {
		t.Fatal(err)
	}
	if live[ids["SHA256:old"]] || !live[ids["SHA256:new"]] || !live[ids["SHA256:ever"]] {
		t.Fatalf("live = %v", live)
	}
	keys, err := s.ListSSHKeys(uid)
	if err != nil || len(keys) != 3 || keys[2].ExpiresAt != nil {
		t.Fatalf("list: %+v %v", keys, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run TestKeyExpiry -count=1`
Expected: FAIL to compile, `unknown field ExpiresAt in struct literal of type KeyOrigin`.

- [ ] **Step 3: Migration**

`0061_ssh_key_expiry.up.sql`:

```sql
-- When the key stops authenticating; NULL for never.
ALTER TABLE ssh_keys ADD COLUMN expires_at TEXT;
```

`0061_ssh_key_expiry.down.sql`:

```sql
ALTER TABLE ssh_keys DROP COLUMN expires_at;
```

- [ ] **Step 4: Store**

`SSHKey` gains, after `CreatedBy`:

```go
	ExpiresAt   *time.Time // nil when the key never expires
```

and the method:

```go
// Expired reports whether the key has lapsed at now.
func (k SSHKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !k.ExpiresAt.After(now)
}
```

`KeyOrigin`:

```go
// KeyOrigin is how a key came to be.
type KeyOrigin struct {
	CreatedByToken int64      // the API token that added it; 0 for none
	ExpiresAt      *time.Time // when it stops authenticating; nil for never
}
```

`AddSSHKeyFrom`'s insert:

```go
	var exp any
	if o.ExpiresAt != nil {
		exp = fmtTime(*o.ExpiresAt)
	}
	if _, err := tx.Exec(
		"INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope, label, created_by_token, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		userID, fingerprint, algo, blob, scope, label, nullID(o.CreatedByToken), exp); err != nil {
```

`SSHKeyByFingerprint` and `SSHKeyByID` select `expires_at` last and scan it through a `sql.NullString`:

```go
func (s *Store) SSHKeyByFingerprint(fingerprint string) (SSHKey, error) {
	var k SSHKey
	var exp sql.NullString
	err := s.DB.QueryRow(
		"SELECT id, user_id, fingerprint, algo, blob, scope, label, expires_at FROM ssh_keys WHERE fingerprint = ?",
		fingerprint).Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope, &k.Label, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	k.ExpiresAt = parseTime(exp)
	return k, err
}
```

`SSHKeyByID` is the same with `WHERE id = ?` and `id`.

`ListSSHKeys`: add `k.expires_at` after `COALESCE(t.name, '')`, scan into `var exp sql.NullString` declared per row, then `k.ExpiresAt = parseTime(exp)`.

`ListDeployKeys`:

```go
func (s *Store) ListDeployKeys(repoID int64) ([]SSHKey, error) {
	rows, err := s.DB.Query(
		`SELECT id, user_id, fingerprint, algo, blob, scope, label, COALESCE(last_used_at, ''), expires_at
		 FROM ssh_keys WHERE scope LIKE 'deploy:' || ? || ':%' ORDER BY id`,
		repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		var exp sql.NullString
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope, &k.Label, &k.LastUsedAt, &exp); err != nil {
			return nil, err
		}
		k.ExpiresAt = parseTime(exp)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
```

`LiveSSHKeys` in `revoke.go`:

```go
// LiveSSHKeys reports which of ids still name a registered, unexpired
// key on an account that is not disabled.
func (s *Store) LiveSSHKeys(ids []int64) (map[int64]bool, error) {
	live := map[int64]bool{}
	if len(ids) == 0 {
		return live, nil
	}
	args := []any{fmtTime(time.Now())}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.DB.Query(`SELECT k.id FROM ssh_keys k JOIN users u ON u.id = k.user_id
		WHERE u.disabled = 0 AND (k.expires_at IS NULL OR k.expires_at > ?)
		AND k.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}
```

Add `"time"` to `revoke.go`'s imports.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/store -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -S -m "store: ssh_keys.expires_at; expired keys are not live

Ref #277"
```

### Task 3.2: expired keys refused at authentication, per exec, and in system mode

**Files:**
- Modify: `internal/sshd/sshd.go` — `authenticate` (after line 148), `runExec`, `Exec` (the `Ctx` literal)
- Modify: `cmd/gitbayd/system.go:45-48`, `:80-84`
- Test: `internal/sshd/revoke_test.go` (append)

**Interfaces:**
- Consumes: `SSHKey.Expired`, `SSHKey.ExpiresAt` (Task 3.1); `Ctx.Expires` (MR 2); `newTestServer`, `execStatus`, `waitClosed` (MR 1).

- [ ] **Step 1: Write the failing tests**

Append to `internal/sshd/revoke_test.go`:

```go
// A key that expires while connected: the next exec is refused, and
// the sweep closes the connection.
func TestExpiredKeyRefusedAndCut(t *testing.T) {
	ts := newTestServer(t)
	past := time.Now().Add(-time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET expires_at = ? WHERE id = ?", past, ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "expired") {
		t.Fatalf("whoami with an expired key: %d %q", code, errOut)
	}
	ts.srv.sweepOnce()
	waitClosed(t, ts.client)
}

// An expiring key may not mint.
func TestExpiringKeyCannotMint(t *testing.T) {
	ts := newTestServer(t)
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET expires_at = ? WHERE id = ?", future, ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "token create --name x"); code != 4 || !strings.Contains(errOut, "expires") {
		t.Fatalf("token create with an expiring key: %d %q", code, errOut)
	}
}
```

And a handshake test, which needs its own key: add to `revoke_test.go`

```go
func TestExpiredKeyRefusedAtAuth(t *testing.T) {
	ts := newTestServer(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	past := time.Now().Add(-time.Minute)
	if err := ts.st.AddSSHKeyFrom(ts.uid, ssh.FingerprintSHA256(pub), pub.Type(), pub.Marshal(), "full", "", store.KeyOrigin{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	_, err = ssh.Dial("tcp", ts.client.RemoteAddr().String(), &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err == nil {
		t.Fatal("an expired key authenticated")
	}
}
```

with `"crypto/ed25519"`, `"crypto/rand"` and `"gitbay.org/gitbay/internal/store"` in the imports.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/sshd -run 'TestExpiredKey|TestExpiringKeyCannotMint' -count=1`
Expected: FAIL: the expired key authenticates and whoami exits 0.

- [ ] **Step 3: sshd**

In `authenticate`, after the `if err != nil { ... }` block that handles unknown keys and before `s.authLimiter.success(ip)`:

```go
	if key.Expired(time.Now()) {
		s.st.Audit(key.UserID, "auth.expired", map[string]any{"ip": ip, "fingerprint": fp})
		return nil, fmt.Errorf("key %s has expired", fp)
	}
```

In `runExec`, after the lookup's error handling and before `UserByID`:

```go
	if key.Expired(time.Now()) {
		fmt.Fprintln(ch.Stderr(), "this key has expired; remove it and add a new one")
		return protocol.ExitDenied
	}
```

In `Exec`, the `Ctx` literal gains `Expires: key.ExpiresAt,`.

- [ ] **Step 4: System mode**

`cmd/gitbayd/system.go`, `authorized-keys`:

```go
			key, err := st.SSHKeyByFingerprint(ssh.FingerprintSHA256(pub))
			if err != nil || key.Expired(time.Now()) {
				return nil // unknown or expired key: no output, auth fails
			}
```

`shell`, after the `SSHKeyByID` error check:

```go
			if key.Expired(time.Now()) {
				fmt.Fprintln(os.Stderr, "this key has expired; remove it and add a new one")
				os.Exit(protocol.ExitDenied)
			}
```

Add `"time"` to the imports.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go test ./internal/sshd -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sshd cmd/gitbayd/system.go
git commit -S -m "sshd: refuse expired keys at auth and per exec; expiring keys cannot mint

Ref #277"
```

### Task 3.3: `--ttl` on `keys add` and `repo deploy-key add`; lists show last use and expiry

**Files:**
- Modify: `internal/control/token.go` (add `ttlFlag` after `parseTTL`)
- Modify: `internal/control/identity.go` — `keys add` registration (33-44), `runKeysList`, `runKeysAdd`
- Modify: `internal/control/deploykey.go` — registration (16-23), `runDeployKeyAdd` (36-80), `runDeployKeyList` (82-115)
- Modify: `e2e/ssh_test.go:267`, `:276`
- Test: `internal/control/keyexpiry_test.go` (create)

**Interfaces:**
- Produces:
  - `func (c *Ctx) ttlFlag(f flags) (*time.Time, int)` — nil when `--ttl` is absent; code -1 when the caller may go on.
  - `func (c *Ctx) usedText(ts string) string`, `func expiresText(t *time.Time, now time.Time) string`

- [ ] **Step 1: Write the failing test**

`internal/control/keyexpiry_test.go`:

```go
package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// authorizedKey is a fresh public key as an authorized_keys line.
func authorizedKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment + "\n"
}

func TestKeysAddTTLAndList(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	user := store.User{ID: uid, Username: "alice"}
	run := func(stdin string, argv ...string) (string, string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), user)
		c.Cfg.Limits.WriteRate = -1
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return c.Stdout.(*bytes.Buffer).String(), errOut.String(), code
	}
	if _, errOut, code := run(authorizedKey(t, "laptop"), "keys", "add", "--ttl", "1h"); code != protocol.ExitOK {
		t.Fatalf("keys add --ttl: %d %s", code, errOut)
	}
	if _, errOut, code := run(authorizedKey(t, "ci"), "repo", "deploy-key", "add", repo.Path(), "--ttl", "2d"); code != protocol.ExitOK {
		t.Fatalf("deploy-key add --ttl: %d %s", code, errOut)
	}
	if _, _, code := run(authorizedKey(t, "x"), "keys", "add", "--ttl", "soon"); code != protocol.ExitUsage {
		t.Fatalf("bad ttl: exit %d", code)
	}

	keys, err := st.ListSSHKeys(uid)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	for _, k := range keys {
		if k.ExpiresAt == nil || k.ExpiresAt.Before(time.Now()) || k.ExpiresAt.After(time.Now().Add(49*time.Hour)) {
			t.Errorf("%s expires %v", k.Label, k.ExpiresAt)
		}
	}
	out, _, _ := run("", "keys", "list")
	if !strings.Contains(out, "\tlaptop\tnever used\texpires ") {
		t.Fatalf("keys list:\n%s", out)
	}
	out, _, _ = run("", "repo", "deploy-key", "list", repo.Path())
	if !strings.Contains(out, "\tci\tnever used\texpires ") {
		t.Fatalf("deploy-key list:\n%s", out)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestKeysAddTTLAndList -count=1`
Expected: FAIL, `keys add --ttl` exits 2 (unknown flag).

- [ ] **Step 3: Helpers**

In `internal/control/token.go` after `parseTTL`:

```go
// ttlFlag reads --ttl as an expiry; nil when the flag is absent. The
// code is -1 when the caller may go on.
func (c *Ctx) ttlFlag(f flags) (*time.Time, int) {
	if !f.Has("--ttl") {
		return nil, -1
	}
	d, err := parseTTL(f.Value("--ttl"))
	if err != nil || d <= 0 {
		return nil, c.fail(protocol.ExitUsage, "bad ttl %q: give a duration such as 30d or 720h", f.Value("--ttl"))
	}
	t := time.Now().Add(d)
	return &t, -1
}
```

In `internal/control/identity.go` after `keyLabel`:

```go
// usedText is a key's last use as a list shows it.
func (c *Ctx) usedText(ts string) string {
	switch {
	case ts == "":
		return "never used"
	case c.Term.Cols == 0:
		return "used " + stamp(ts)
	}
	return "used " + relAge(ts, termNow())
}

// expiresText is a credential's expiry as a list shows it. It is
// absolute at a terminal too: relAge reads only the past.
func expiresText(t *time.Time, now time.Time) string {
	if t == nil {
		return "never expires"
	}
	s := stamp(t.UTC().Format(time.RFC3339Nano))
	if !t.After(now) {
		return "expired " + s
	}
	return "expires " + s
}
```

Add `"time"` to `identity.go`'s imports.

- [ ] **Step 4: `keys add --ttl`**

Registration:

```go
	register(Command{
		Path:    []string{"keys", "add"},
		Summary: "register an SSH public key (authorized_keys format)",
		Usage:   "keys add [--scope full|git|runner] [--label <text>] [--ttl 30d|720h] < key.pub",
		Flags: []Flag{
			{"--scope", "full|git|runner", "what the key may do", "full"},
			{"--label", "<text>", "a name for the key", ""},
			{"--ttl", "30d|720h", "how long the key authenticates; an expiring key cannot mint credentials", "never expires"},
		},
		Examples:        []string{"keys add --label laptop < key.pub", "keys add --scope git --ttl 90d < ci.pub"},
		ReadsStdin:      true,
		MintsCredential: true,
		Run:             runKeysAdd,
	})
```

In `runKeysAdd`: `parseFlags(args, flagSpec{Values: []string{"--scope", "--label", "--ttl"}, MaxPos: 0, Usage: c.Cmd.Usage})`; after the scope check:

```go
	expires, code := c.ttlFlag(f)
	if code >= 0 {
		return code
	}
```

the store call:

```go
	if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label, store.KeyOrigin{CreatedByToken: c.TokenID, ExpiresAt: expires}); err != nil {
```

and the output:

```go
	type out struct {
		Fingerprint string     `json:"fingerprint"`
		Scope       string     `json:"scope"`
		Label       string     `json:"label"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	}
	d := out{fp, scope, label, expires}
	return c.emit(d, func(w io.Writer) {
		line := fmt.Sprintf("added %s (%s)", d.Fingerprint, d.Scope)
		if d.Label != "" {
			line += " " + d.Label
		}
		if d.ExpiresAt != nil {
			line += ", " + expiresText(d.ExpiresAt, time.Now())
		}
		fmt.Fprintln(w, line)
	})
```

- [ ] **Step 5: `keys list` columns**

```go
	type out struct {
		Fingerprint string     `json:"fingerprint"`
		Algo        string     `json:"algo"`
		Scope       string     `json:"scope"`
		Label       string     `json:"label"`
		CreatedBy   string     `json:"created_by,omitempty"`
		LastUsedAt  string     `json:"last_used_at,omitempty"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	}
	var ds []out
	for _, k := range keys {
		ds = append(ds, out{k.Fingerprint, k.Algo, k.Scope, k.Label, k.CreatedBy, k.LastUsedAt, k.ExpiresAt})
	}
	now := time.Now()
	return c.emit(ds, func(w io.Writer) {
		tb := c.table(w, "FINGERPRINT", "ALGO", "SCOPE", "LABEL", "USED", "EXPIRES")
		for _, d := range ds {
			tb.row(cFlex(d.Fingerprint), cText(d.Algo), cState(d.Scope), cText(d.Label),
				cText(c.usedText(d.LastUsedAt)), cText(expiresText(d.ExpiresAt, now)))
		}
		tb.flush()
	})
```

- [ ] **Step 6: `repo deploy-key add --ttl`, list columns**

Registration:

```go
	register(Command{Path: []string{"repo", "deploy-key", "add"},
		Summary: "bind a read-only (or --rw) key to one repository",
		Usage:   "repo deploy-key add <owner/name> [--rw] [--ttl 30d|720h] < key.pub",
		Flags: []Flag{
			{"--rw", "", "the key may push, not just fetch", ""},
			{"--ttl", "30d|720h", "how long the key authenticates", "never expires"},
		},
		Examples:        []string{"repo deploy-key add krz/gitbay < key.pub", "repo deploy-key add krz/gitbay --ttl 30d < key.pub"},
		ReadsStdin:      true,
		MintsCredential: true,
		Run:             runDeployKeyAdd})
```

`runDeployKeyAdd`, replacing its argument loop (lines 37-52):

```go
func runDeployKeyAdd(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--ttl"}, Bools: []string{"--rw"}, MaxPos: 1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path := f.pos(0)
	if path == "" {
		return c.usage()
	}
	mode := "ro"
	if f.Has("--rw") {
		mode = "rw"
	}
	expires, code := c.ttlFlag(f)
	if code >= 0 {
		return code
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
```

The rest is as before except the store call and output:

```go
	if err := c.Store.AddSSHKeyFrom(c.User.ID, fp, pub.Type(), pub.Marshal(), scope, label, store.KeyOrigin{CreatedByToken: c.TokenID, ExpiresAt: expires}); err != nil {
		if errors.Is(err, store.ErrDuplicateKey) {
			return c.failErr(err)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d := map[string]any{"fingerprint": fp, "mode": mode}
	if expires != nil {
		d["expires_at"] = expires
	}
	return c.emit(d, func(w io.Writer) {
		line := fmt.Sprintf("deploy key %s (%s) bound to %s", fp, mode, repo.Path())
		if expires != nil {
			line += ", " + expiresText(expires, time.Now())
		}
		fmt.Fprintln(w, line)
	})
```

`runDeployKeyList`:

```go
	type out struct {
		Fingerprint string     `json:"fingerprint"`
		Algo        string     `json:"algo"`
		Mode        string     `json:"mode"`
		Label       string     `json:"label"`
		LastUsedAt  string     `json:"last_used_at,omitempty"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	}
	var ds []out
	for _, k := range keys {
		mode := "ro"
		if policy.DeployScopeAllows(k.Scope, repo.ID, true) {
			mode = "rw"
		}
		ds = append(ds, out{k.Fingerprint, k.Algo, mode, k.Label, k.LastUsedAt, k.ExpiresAt})
	}
	now := time.Now()
	return c.emit(ds, func(w io.Writer) {
		tb := c.table(w, "FINGERPRINT", "ALGO", "MODE", "LABEL", "USED", "EXPIRES")
		for _, d := range ds {
			tb.row(cFlex(d.Fingerprint), cText(d.Algo), cState(d.Mode), cText(d.Label),
				cText(c.usedText(d.LastUsedAt)), cText(expiresText(d.ExpiresAt, now)))
		}
		tb.flush()
	})
```

Add `"time"` to `deploykey.go`'s imports.

- [ ] **Step 7: The e2e rows that end at the label**

`e2e/ssh_test.go:267`: `if !strings.Contains(out, "\tgit\talice2\t") {`
`e2e/ssh_test.go:276`: `if !strings.Contains(out, "\tgit\tbuild box\t") {`

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/control ./internal/store ./internal/sshd -count=1 && go test ./e2e -run TestSSHControlPlane -count=1`

(Check the name of the test holding `e2e/ssh_test.go:255-276` with `grep -n "^func Test" e2e/ssh_test.go` and run that one.)
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/control e2e/ssh_test.go
git commit -S -m "keys: --ttl on keys add and repo deploy-key add; lists show last use and expiry

Ref #277"
```

### Task 3.4: docs

**Files:**
- Modify: `.gitbay/wiki/Users.org` (keys block ~61-66 and the scopes paragraph), `.gitbay/wiki/Architecture/05-Identity-and-Access.org:17-18`, `09-Controls.org:27`, `10-Known-Gaps.org`, `.gitbay/wiki/Parity.org` (Accounts), `.gitbay/wiki/API.org` (the paragraph added in MR 2), `CHANGELOG.org`

- [ ] **Step 1: Edit the pages**

`Users.org`, add to the keys code block:

```
gitbay auth keys add --scope git --ttl 90d < ~/.ssh/ci_key.pub
```

and after the labels paragraph:

```
=--ttl 90d= (or any Go duration, =720h=) makes a key stop
authenticating after that long; =repo deploy-key add= takes the same
flag. An expiring key cannot create credentials: tokens, keys, login
links. =keys list= shows when each key was last used and when it
expires, so a key nobody uses is easy to spot.
```

`05-Identity-and-Access.org`: SSH user key and deploy key Expiry cells become `optional =--ttl=, refused at auth`.

`09-Controls.org`:

```
| Credential expiry                           | in place | optional =--ttl= on API tokens, SSH and deploy keys; checked at auth and per exec |
```

`10-Known-Gaps.org`: delete the `#277` row.

`Parity.org`, after `SSH key label`:

```
| SSH key expiry and last use | yes | no  | no  |
```

`API.org`: in the paragraph MR 2 added, "a token with a =--ttl= cannot run" becomes "a token or SSH key with a =--ttl= cannot run".

`CHANGELOG.org`:

```
- =keys add= and =repo deploy-key add= take =--ttl=; an expired key is
  refused at authentication, and an open connection on it closes within
  15 seconds. An expiring key cannot create credentials, like an
  expiring token. =keys list= and =repo deploy-key list= gain =USED= and
  =EXPIRES= columns, after the label (#277).
```

- [ ] **Step 2: Commit, open the MR**

```bash
git add .gitbay/wiki CHANGELOG.org
git commit -S -m "wiki: key expiry

Closes #277"
git push -u origin key-expiry
gitbay mr create --source key-expiry --target main --title "keys: optional expiry for SSH and deploy keys"
```

---

# MR 4: idle timeout for browser sessions (branch `session-idle`, #276)

A session lapses after 12 hours without a request and after seven
days regardless. `expires_at` holds the sliding expiry, so the auth
query and the retention sweep (`expires_at <= ?`,
`internal/store/retention.go:51`) need no change;
`absolute_expires_at` holds the cap. Renewal writes at most once a
minute per session. The cookie's `MaxAge` stays seven days.

### Task 4.1: migration 0062 and the store

**Files:**
- Create: `internal/store/migrations/0062_web_session_idle.up.sql`, `.down.sql`
- Modify: `internal/store/sessions.go:67-121`
- Test: `internal/store/sessions_test.go` (append)

**Interfaces:**
- Produces:
  - `const WebSessionIdle = 12 * time.Hour`
  - `CreateWebSession(hash string, userID int64, ttl time.Duration) error` — unchanged signature; `ttl` is now the absolute cap.
  - `WebSessionUser(hash string) (User, error)` — unchanged signature; renews.
  - `WebSession.LastUsedAt string `json:"last_used_at"``

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/sessions_test.go`:

```go
func sessionFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", false)
	if err != nil {
		t.Fatal(err)
	}
	return s, uid
}

func sessionTimes(t *testing.T, s *Store, hash string) (expires, absolute time.Time) {
	t.Helper()
	var e, a string
	if err := s.DB.QueryRow("SELECT expires_at, absolute_expires_at FROM web_sessions WHERE token_hash = ?", hash).Scan(&e, &a); err != nil {
		t.Fatal(err)
	}
	return *parseTime(sql.NullString{String: e, Valid: true}), *parseTime(sql.NullString{String: a, Valid: true})
}

func TestWebSessionIdleExpiry(t *testing.T) {
	s, uid := sessionFixture(t)
	if err := s.CreateWebSession("h", uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	exp, abs := sessionTimes(t, s, "h")
	if d := time.Until(exp); d < WebSessionIdle-time.Minute || d > WebSessionIdle {
		t.Fatalf("a new session expires in %s, want %s", d, WebSessionIdle)
	}
	if d := time.Until(abs); d < 7*24*time.Hour-time.Minute {
		t.Fatalf("absolute cap in %s", d)
	}
	// Idle past the window: gone.
	old := fmtTime(time.Now().Add(-time.Second))
	s.DB.Exec("UPDATE web_sessions SET expires_at = ? WHERE token_hash = 'h'", old)
	if _, err := s.WebSessionUser("h"); err != ErrNotFound {
		t.Fatalf("idle session: %v", err)
	}
}

func TestWebSessionRenewsUpToTheCap(t *testing.T) {
	s, uid := sessionFixture(t)
	if err := s.CreateWebSession("h", uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Last used two minutes ago, one minute left: a request renews it.
	s.DB.Exec("UPDATE web_sessions SET last_used_at = ?, expires_at = ? WHERE token_hash = 'h'",
		fmtTime(time.Now().Add(-2*time.Minute)), fmtTime(time.Now().Add(time.Minute)))
	if _, err := s.WebSessionUser("h"); err != nil {
		t.Fatal(err)
	}
	if exp, _ := sessionTimes(t, s, "h"); time.Until(exp) < WebSessionIdle-time.Minute {
		t.Fatalf("not renewed: expires in %s", time.Until(exp))
	}
	// Near the cap, renewal stops at it.
	capAt := time.Now().Add(time.Hour)
	s.DB.Exec("UPDATE web_sessions SET last_used_at = ?, absolute_expires_at = ? WHERE token_hash = 'h'",
		fmtTime(time.Now().Add(-2*time.Minute)), fmtTime(capAt))
	if _, err := s.WebSessionUser("h"); err != nil {
		t.Fatal(err)
	}
	if exp, _ := sessionTimes(t, s, "h"); exp.After(capAt) {
		t.Fatalf("renewed past the cap: %s > %s", exp, capAt)
	}
	list, err := s.ListWebSessions(uid)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == "" {
		t.Fatalf("list: %+v %v", list, err)
	}
}
```

Add `"database/sql"` to the file's imports.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/store -run 'TestWebSession' -count=1`
Expected: FAIL to compile, `undefined: WebSessionIdle`.

- [ ] **Step 3: Migration**

`0062_web_session_idle.up.sql`:

```sql
-- expires_at slides forward on use, never past absolute_expires_at.
-- Sessions open now keep their cap and get a full idle window from here.
ALTER TABLE web_sessions ADD COLUMN absolute_expires_at TEXT;
ALTER TABLE web_sessions ADD COLUMN last_used_at TEXT;
UPDATE web_sessions SET
    absolute_expires_at = expires_at,
    last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    expires_at = min(expires_at, strftime('%Y-%m-%dT%H:%M:%fZ','now','+12 hours'));
```

`0062_web_session_idle.down.sql`:

```sql
UPDATE web_sessions SET expires_at = absolute_expires_at;
ALTER TABLE web_sessions DROP COLUMN last_used_at;
ALTER TABLE web_sessions DROP COLUMN absolute_expires_at;
```

- [ ] **Step 4: Store**

Replace `CreateWebSession` and `WebSessionUser`:

```go
// WebSessionIdle is how long a browser session lasts without a request.
// Each use moves its expiry this far ahead, never past the cap it was
// created with. Migration 0062 repeats the value for sessions it
// converts.
const WebSessionIdle = 12 * time.Hour

// CreateWebSession stores a session that lapses after WebSessionIdle
// without use, and after ttl regardless.
func (s *Store) CreateWebSession(hash string, userID int64, ttl time.Duration) error {
	now := time.Now()
	_, err := s.DB.Exec(
		"INSERT INTO web_sessions (token_hash, user_id, expires_at, absolute_expires_at, last_used_at) VALUES (?, ?, ?, ?, ?)",
		hash, userID, fmtTime(now.Add(min(ttl, WebSessionIdle))), fmtTime(now.Add(ttl)), fmtTime(now))
	return err
}

// WebSessionUser resolves a session cookie hash to its user and renews
// the session's idle expiry. A session is written at most once a
// minute, so a burst of requests costs one UPDATE.
func (s *Store) WebSessionUser(hash string) (User, error) {
	now := time.Now()
	var userID int64
	err := s.DB.QueryRow(
		"SELECT user_id FROM web_sessions WHERE token_hash = ? AND expires_at > ?",
		hash, fmtTime(now)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	s.DB.Exec(`UPDATE web_sessions SET last_used_at = ?, expires_at = min(absolute_expires_at, ?)
		WHERE token_hash = ? AND last_used_at < ?`,
		fmtTime(now), fmtTime(now.Add(WebSessionIdle)), hash, fmtTime(now.Add(-time.Minute)))
	return s.UserByID(userID)
}
```

`WebSession` and `ListWebSessions`:

```go
type WebSession struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	LastUsedAt string `json:"last_used_at"`
}

// ListWebSessions lists the user's unexpired browser sessions, newest first.
func (s *Store) ListWebSessions(userID int64) ([]WebSession, error) {
	rows, err := s.DB.Query(`SELECT substr(token_hash, 1, 12), created_at, expires_at, COALESCE(last_used_at, created_at)
		FROM web_sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC`,
		userID, fmtTime(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSession
	for rows.Next() {
		var ws WebSession
		if err := rows.Scan(&ws.ID, &ws.CreatedAt, &ws.ExpiresAt, &ws.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/store -count=1`
Expected: PASS, including `TestSweepRemovesExpiredSessionsAndTokens` (a `-time.Hour` ttl still makes a dead session).

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -S -m "store: web sessions lapse after 12 hours idle, under the absolute cap

Ref #276"
```

### Task 4.2: `web sessions list` shows last use; docs

**Files:**
- Modify: `internal/control/web.go:33-54`
- Modify: `internal/httpd/accounts.go:147` (comment only)
- Modify: `.gitbay/wiki/Users.org:672-678`, `.gitbay/wiki/Architecture/05-Identity-and-Access.org:21`, `:36-38`, `09-Controls.org:26`, `10-Known-Gaps.org`, `CHANGELOG.org`

- [ ] **Step 1: The list**

```go
	return c.emit(sessions, func(w io.Writer) {
		tb := c.table(w, "ID", "SINCE", "UNTIL", "USED")
		for _, s := range sessions {
			since, until, used := s.CreatedAt, s.ExpiresAt, s.LastUsedAt
			if c.Term.Cols == 0 {
				since, until, used = stamp(since), stamp(until), stamp(used)
			} else {
				since, until, used = relAge(since, termNow()), relAge(until, termNow()), relAge(used, termNow())
			}
			tb.row(cRef(s.ID), cText("since "+since), cText("until "+until), cText("used "+used))
		}
		tb.flush()
	})
```

- [ ] **Step 2: The call site says what the ttl is**

`internal/httpd/accounts.go`, above line 147:

```go
	// Seven days is the cap; the store ends it sooner after
	// store.WebSessionIdle without a request.
```

- [ ] **Step 3: Run the tests**

Run: `go build ./... && go test ./internal/control ./internal/httpd -count=1 && go test ./e2e -run TestWebSessionsListRevoke -count=1`
Expected: PASS.

- [ ] **Step 4: Docs**

`Users.org`, the Browser sessions paragraph:

```
=gitbay web login= mints a one-time URL; the session it opens ends
after twelve hours without a request, and after seven days in any case.
=gitbay web sessions list= shows each of yours by a short id with its
creation, expiry and last use, and =gitbay web sessions revoke <id>=
or =--all= ends them from the terminal, which is where a lost laptop is
handled.
```

`05-Identity-and-Access.org`: the Web session Expiry cell becomes `12 h idle, 7 days absolute`; in the paragraph under the table, "=MaxAge= 7 days" becomes "=MaxAge= 7 days (the session itself also ends after 12 hours idle)".

`09-Controls.org`:

```
| Session lifetime                            | in place | 12 hours idle, 7 days absolute (=internal/store/sessions.go=)            |
```

`10-Known-Gaps.org`: delete the `#276` row.

`CHANGELOG.org`:

```
- Browser sessions end after twelve hours without a request, and after
  seven days as before. Sessions open at upgrade get a fresh twelve
  hours. =web sessions list= shows when each was last used (#276).
```

- [ ] **Step 5: Commit, open the MR**

```bash
git add internal/control/web.go internal/httpd/accounts.go .gitbay/wiki CHANGELOG.org
git commit -S -m "web: sessions list shows last use; docs for the idle timeout

Closes #276"
git push -u origin session-idle
gitbay mr create --source session-idle --target main --title "web: idle timeout for browser sessions"
```

---

# MR 5: `web login` over SSH spends the login-link budget (branch `weblogin-limit`, #278)

### Task 5.1: the limit in `runWebLogin`

**Files:**
- Modify: `internal/control/web.go:80-99`
- Modify: `internal/control/loginlink.go:12-19` (comment)
- Test: `internal/control/weblogin_test.go` (create)
- Modify: `.gitbay/wiki/Architecture/10-Known-Gaps.org`, `CHANGELOG.org`

**Interfaces:**
- Consumes: `maxLoginLinksPerHour` (`loginlink.go:19`), `Store.CountLoginTokensSince`.

- [ ] **Step 1: Write the failing test**

```go
package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// web login over SSH counts against the same hourly bound as the
// mailed links, since both insert into login_tokens (#278).
func TestWebLoginSharesTheLoginLinkLimit(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	for i := 0; i <= maxLoginLinksPerHour; i++ {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		c.Cfg.Web.Mode = "accounts"
		c.Cfg.Server.SiteURL = "https://gitbay.test"
		c.Cfg.Limits.WriteRate = -1
		code := Dispatch(c, []string{"web", "login"})
		switch {
		case i < maxLoginLinksPerHour && code != protocol.ExitOK:
			t.Fatalf("link %d: exit %d %s", i+1, code, errOut)
		case i == maxLoginLinksPerHour && (code != protocol.ExitDenied || !strings.Contains(errOut.String(), "login links")):
			t.Fatalf("link %d: exit %d %q, want refused", i+1, code, errOut)
		}
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestWebLoginSharesTheLoginLinkLimit -count=1`
Expected: FAIL, the sixth link exits 0.

- [ ] **Step 3: Implement**

In `runWebLogin`, after the web-mode check:

```go
	n, err := c.Store.CountLoginTokensSince(c.User.ID, time.Now().Add(-time.Hour))
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if n >= maxLoginLinksPerHour {
		return c.fail(protocol.ExitDenied,
			"%d login links in the last hour is the most an account gets; use one of those, or wait", maxLoginLinksPerHour)
	}
```

`loginlink.go`, the comment above `maxLoginLinksPerHour`:

```go
// maxLoginLinksPerHour bounds what one account's address can be made to
// receive. It matches maxEmailAddsPerHour: enough for a person who mistypes
// and retries, nothing for a script. CountLoginTokensSince counts every row
// in login_tokens, so links minted with "web login" over SSH and links
// mailed from the login page share the budget, and both refuse past it.
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/control -count=1`
Expected: PASS. Then `grep -c '\.login(t\|loginBrowser(t\|"web", "login"' e2e/*.go` and confirm no single e2e test logs one account in more than five times; `TestMRWebReviewLoop` logs alice in once and carol once, and each e2e test runs its own instance.

- [ ] **Step 5: Docs**

`10-Known-Gaps.org`: delete the `#278` row.

`CHANGELOG.org`:

```
- =web login= over SSH refuses a sixth link in an hour, the same bound
  the login page's mailed links have (#278).
```

- [ ] **Step 6: Commit, open the MR**

```bash
git add internal/control/web.go internal/control/loginlink.go internal/control/weblogin_test.go .gitbay/wiki CHANGELOG.org
git commit -S -m "web: login over SSH applies the login-link limit

Closes #278"
git push -u origin weblogin-limit
gitbay mr create --source weblogin-limit --target main --title "web login over SSH applies the login-link rate limit"
```

---

## Open questions

1. **Expiring SSH keys and minting.** #277 says to consider it with
   #257; this plan treats an expiring key like an expiring token and
   refuses it the nine minting commands (MR 3, Task 3.2). One effect:
   a person whose only key has a TTL cannot run `web login`, and must
   use the mailed link. Confirm, or drop `Expires: key.ExpiresAt` from
   `Exec` and `TestExpiringKeyCannotMint`.
2. **Which commands mint.** The issue names token create, keys add,
   repo deploy-key add, admin invite and web login. The plan adds
   `repo runner add` (creates or attaches a key that claims builds),
   `admin user create` (with `--key` or a verified address it is a way
   in), `email verify` and `admin email verify` (a verified address
   receives login links, so a token could plant a lasting way in).
   `TestMintingCommandsMarked` pins the list.
3. **LFS transfer tokens.** `git-lfs-authenticate` hands out an HMAC
   token valid for an hour (`internal/lfs`), not stored, revocable only
   by expiry. A key removed after minting one leaves LFS access for up
   to an hour. Not in any of these issues; file separately?
4. **System mode.** With `ssh.mode = "system"`, each exec is its own
   forced-command process: the per-exec check applies, a running one is
   not cut. bay1 runs embedded. Closing it would take a poll in
   `gitbayd shell`; the plan documents the limit instead.
5. **Commands already past dispatch.** "Running commands included" is
   implemented as: git transports are killed, commands watching `Done`
   stop, and a control command already inside its store write finishes
   it with its output lost. Cancelling arbitrary control commands would
   need a context threaded through every handler.
6. **Removing the key you are on.** `keys remove <the key this session
   uses>` cuts its own connection, so the CLI reports a connection
   error instead of "removed". The removal has committed. Acceptable,
   or should `revoke` skip the connection running the removal?
7. **Idle window as a constant.** 12 hours is `store.WebSessionIdle`,
   repeated in migration 0062. Should it be configurable?

Noticed, not in scope: `token list` at a terminal shows a future
expiry through `relAge`, which clamps to zero and prints "just now"
(`internal/control/token.go:112-116`). `expiresText` (MR 3) would fix
it if applied there.

## Self-review

- **Coverage.** #256: per-exec re-read (Task 1.3 `runExec`), git
  transport session re-read (same path; `Exec` dispatches git),
  connection tracking and closing on keys remove / deploy-key remove /
  disable / delete (1.1, 1.3), cancelling running commands and pushes
  (1.2, 1.3), interrupted receive-pack moves no ref (1.4), e2e with a
  multiplexed connection (1.4). #257: `MintsCredential` checked in
  `Dispatch` (2.2), migration recording the creator for tokens and keys
  (2.1), `token revoke` listing and revoking with `--created` (2.1,
  2.3), default `--scope read` and release note (2.3, 2.5), API and
  Threat-Model pages (2.5). #277: `--ttl` on both add commands (3.3),
  enforced at authentication (3.2), last use in `keys list` (3.3),
  considered with #257 (3.2, open question 1), migration designed with
  0060 (both nullable `ADD COLUMN`, one insert path via `KeyOrigin`).
  #276: idle timeout with renewal, absolute cap kept, last use listed
  (4.1, 4.2). #278: limit applied, comment corrected (5.1).
- **Placeholders.** None; every code step carries the code. Two steps
  ask the executor to confirm a name with grep (`nullID`, the
  `ssh_test.go` test name) because they were not unique facts to pin.
- **Types.** `Revoked{KeyIDs []int64; UserID int64}`, `OnRevoke`,
  `announce`, `LiveSSHKeys([]int64) (map[int64]bool, error)` are used
  with those shapes in 1.1, 1.3, 2.1, 3.1. `Exec(..., key store.SSHKey,
  ..., done, stopping, revoked)` matches in 1.3, 3.2 and `system.go`.
  `APITokenUser` returns `(User, APIToken, error)` in 2.1, 2.2 and the
  tests. `KeyOrigin{CreatedByToken, ExpiresAt}` is introduced in 2.1
  and extended in 3.1. `Ctx.TokenID int64`, `Ctx.Expires *time.Time`
  match across 2.2, 2.3, 3.2, 3.3. `SSHKey.CreatedBy` (name) and
  `KeyOrigin.CreatedByToken` (id) are distinct on purpose.
