# build log --follow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `build log <owner/name> <n> --follow` streams a build's log until the build has an outcome, and the web build page streams the same command without JavaScript.

**Architecture:** The store wakes waiters when a build's row changes and reads the log from a byte offset. The control command loops read → write → wait. The web handler renders `build.html` with a marker where the log goes, writes the part before it, dispatches the command with a writer that HTML-escapes and flushes, then writes the rest.

**Tech Stack:** Go, SQLite (modernc), `golang.org/x/crypto/ssh`, `html/template`, `net/http`.

**Spec:** `docs/specs/2026-09-23-build-log-follow-design.md`

## Global Constraints

- Work in `/Users/cmc/git/krz/gitbay-follow` (branch `build-log-follow`). Never touch `/Users/cmc/git/krz/gitbay`.
- Every commit is signed (the repo signs by config; do not pass `--no-gpg-sign`). Messages reference `Ref #250`; the last commit says `Closes #250`. No attribution lines of any kind.
- Write files with the editor tool, not heredocs.
- Locally run: `go build ./...`, `go vet` on touched packages, unit tests of touched packages, and only the one e2e test being written. CI runs the rest.
- Comments: plain, factual, match the surrounding density. No before/after narration.
- Follow cap: 8 per account. Fallback re-read: 2 seconds. Settle after an outcome: 1 second.
- The outcome line on stderr is exactly `build <n> <status>`; exit 0 whatever the outcome.
- The web outcome line is exactly `<p class="notice" role="status">build finished: <status></p>`.

---

### Task 1: Store — wake followers and read from an offset

**Files:**
- Modify: `internal/store/store.go` (the `Store` struct, ~line 22)
- Modify: `internal/store/builds.go` (`AppendBuildLog` ~228, `FinishBuild` ~248, `CancelBuild` ~393; new functions after `BuildLog` ~325)
- Test: `internal/store/builds_test.go`

**Interfaces:**
- Produces: `func (s *Store) BuildLogWait(id int64) <-chan struct{}`; `func (s *Store) BuildLogFrom(id, offset int64) (status string, chunk []byte, err error)`; unexported `func (s *Store) wakeBuild(id int64)`.

- [ ] **Step 1: Write the failing tests** — append to `internal/store/builds_test.go`:

```go
// A follower's channel closes on each kind of change to its build, and
// only its build.
func TestBuildLogWaitWakes(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRepo("user", uid, "orgo", "public"); err != nil {
		t.Fatal(err)
	}
	newBuild := func() int64 {
		t.Helper()
		id, err := s.CreateBuild(1, "test", "abc123", "main", `["true"]`, "", "", true)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	a, b := newBuild(), newBuild()
	wa, wb := s.BuildLogWait(a), s.BuildLogWait(b)
	if err := s.AppendBuildLog(a, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !closed(wa) {
		t.Error("append did not wake its build")
	}
	if closed(wb) {
		t.Error("append woke another build")
	}

	mustClaim(t, s, 1) // claims a, the oldest
	wa = s.BuildLogWait(a)
	if err := s.FinishBuild(a, "success"); err != nil {
		t.Fatal(err)
	}
	if !closed(wa) {
		t.Error("finish did not wake")
	}

	if err := s.CancelBuild(b); err != nil {
		t.Fatal(err)
	}
	if !closed(wb) {
		t.Error("cancel did not wake")
	}
}

// Offsets are bytes, not characters: || stores the log as text, and a
// multibyte character must not shift where the next read starts.
func TestBuildLogFrom(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRepo("user", uid, "orgo", "public"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateBuild(1, "test", "abc123", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	first := "héllo — ok\n"
	for _, c := range []string{first, "wörld\n"} {
		if err := s.AppendBuildLog(id, []byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	status, all, err := s.BuildLogFrom(id, 0)
	if err != nil || status != "pending" || string(all) != first+"wörld\n" {
		t.Fatalf("from 0: %q %q %v", status, all, err)
	}
	_, rest, err := s.BuildLogFrom(id, int64(len(first)))
	if err != nil || string(rest) != "wörld\n" {
		t.Fatalf("from %d: %q %v", len(first), rest, err)
	}
	_, none, err := s.BuildLogFrom(id, int64(len(all)))
	if err != nil || len(none) != 0 {
		t.Fatalf("from the end: %q %v", none, err)
	}
	if _, _, err := s.BuildLogFrom(9999, 0); err != ErrNotFound {
		t.Fatalf("missing build: %v", err)
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/store/ -run 'TestBuildLogWaitWakes|TestBuildLogFrom' -count=1`
Expected: build failure, `s.BuildLogWait undefined`.

- [ ] **Step 3: Implement**

In `internal/store/store.go`, add `"sync"` to the imports and the fields:

```go
type Store struct {
	DB *sql.DB

	// logWait holds one channel per build someone is following, closed
	// by the next change to that build's row (BuildLogWait).
	logMu   sync.Mutex
	logWait map[int64]chan struct{}
}
```

In `internal/store/builds.go`, after `BuildLog`:

```go
// BuildLogWait returns a channel closed by the next append to, finish of
// or cancel of the build. Take it before reading, so a change between the
// read and the wait still wakes the reader. Only this process's writes
// wake it.
func (s *Store) BuildLogWait(id int64) <-chan struct{} {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logWait == nil {
		s.logWait = map[int64]chan struct{}{}
	}
	ch, ok := s.logWait[id]
	if !ok {
		ch = make(chan struct{})
		s.logWait[id] = ch
	}
	return ch
}

func (s *Store) wakeBuild(id int64) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if ch, ok := s.logWait[id]; ok {
		close(ch)
		delete(s.logWait, id)
	}
}

// BuildLogFrom returns the build's status and its log past offset bytes,
// read together so a terminal status comes with every byte before it.
// The cast matters: || stores the log as text, and substr on text counts
// characters.
func (s *Store) BuildLogFrom(id, offset int64) (string, []byte, error) {
	var status string
	var chunk []byte
	err := s.DB.QueryRow(`SELECT status, substr(CAST(log AS BLOB), ?) FROM builds WHERE id = ?`,
		offset+1, id).Scan(&status, &chunk)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return status, chunk, err
}
```

Wake after each successful write. In `AppendBuildLog`, replace the tail so both paths wake:

```go
	if n, _ := res.RowsAffected(); n > 0 {
		s.wakeBuild(id)
		return nil
	}
	// Over the cap. The bounds match exactly once: appending the notice puts
	// the log past the upper bound, so later chunks fall through silently.
	_, err = s.DB.Exec(`
		UPDATE builds SET log = log || ?
		WHERE id = ? AND length(log) >= ? AND length(log) < ?`,
		truncNotice, id, MaxBuildLog, MaxBuildLog+len(truncNotice))
	if err == nil {
		s.wakeBuild(id)
	}
	return err
```

In `FinishBuild`, before the final `return nil`: `s.wakeBuild(id)`. In `CancelBuild`, the same, before its final `return nil`.

- [ ] **Step 4: Run the tests and vet**

Run: `go test ./internal/store/ -count=1 && go vet ./internal/store/`
Expected: `ok`, vet silent (no copylocks: `Store` is only ever `&Store{...}` in `Open`).

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/builds.go internal/store/builds_test.go
git commit -m "store: wake build log followers, read the log from an offset

Ref #250"
```

---

### Task 2: Control — `build log --follow`

**Files:**
- Modify: `internal/control/control.go` (`Ctx`, ~line 21)
- Modify: `internal/control/build.go` (registration ~29, `runBuildLog` ~198)
- Create: `internal/control/buildfollow.go`
- Create: `internal/control/buildfollow_test.go`
- Modify: `cmd/gitbay/main.go:51` (help string)

**Interfaces:**
- Consumes: `Store.BuildLogWait`, `Store.BuildLogFrom` (Task 1).
- Produces: `Ctx.Done <-chan struct{}`; the command `build log <owner/name> <n> [--follow]`; package vars `followSettle`, `followPoll` (tests shorten them); `maxFollows = 8`.

- [ ] **Step 1: Write the failing tests** — `internal/control/buildfollow_test.go`:

```go
package control

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// follow starts build log --follow on build 1 of repo and returns the
// buffers and a channel carrying the exit code.
func follow(t *testing.T, st *store.Store, uid int64, repo store.Repo, done <-chan struct{}) (*bytes.Buffer, *bytes.Buffer, chan int) {
	t.Helper()
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: st, Stdin: strings.NewReader(""),
		Stdout: &out, Stderr: &errOut, Done: done}
	res := make(chan int, 1)
	go func() { res <- Dispatch(c, []string{"build", "log", repo.Path(), "1", "--follow"}) }()
	return &out, &errOut, res
}

func waitExit(t *testing.T, res chan int) int {
	t.Helper()
	select {
	case code := <-res:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end")
		return -1
	}
}

func shortFollowTimers(t *testing.T) {
	settle, poll := followSettle, followPoll
	followSettle, followPoll = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { followSettle, followPoll = settle, poll })
}

// The follow prints the stored log, then what arrives, and ends with the
// outcome on stderr once the build finishes.
func TestBuildLogFollow(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("queued\n"))
	out, errOut, res := follow(t, st, uid, repo, nil)

	if _, ok, err := st.ClaimBuild([]int64{repo.ID}, false); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	st.AppendBuildLog(id, []byte("step two\n"))
	if err := st.FinishBuild(id, "success"); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, res); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := out.String(); got != "queued\nstep one\nstep two\n" {
		t.Errorf("stdout %q", got)
	}
	if got := strings.TrimSpace(errOut.String()); got != "build 1 success" {
		t.Errorf("stderr %q", got)
	}
}

// A cancel ends the follow, and the line the cancel appends after the
// status change still arrives.
func TestBuildLogFollowCancel(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, res := follow(t, st, uid, repo, nil)
	if err := st.CancelBuild(id); err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("cancelled by alice before a runner claimed it\n"))
	if code := waitExit(t, res); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out.String(), "cancelled by alice") {
		t.Errorf("the cancel line did not arrive: %q", out)
	}
	if got := strings.TrimSpace(errOut.String()); got != "build 1 cancelled" {
		t.Errorf("stderr %q", got)
	}
}

// Closing Done ends a follow of a build that is still running.
func TestBuildLogFollowDone(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	_, _, res := follow(t, st, uid, repo, done)
	close(done)
	if code := waitExit(t, res); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d", code, protocol.ExitFailure)
	}
}

// An account holding maxFollows is refused another.
func TestBuildLogFollowCap(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	followMu.Lock()
	follows[uid] = maxFollows
	followMu.Unlock()
	t.Cleanup(func() {
		followMu.Lock()
		delete(follows, uid)
		followMu.Unlock()
	})
	_, errOut, res := follow(t, st, uid, repo, nil)
	if code := waitExit(t, res); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	if !strings.Contains(errOut.String(), "8 follows are already open") {
		t.Errorf("stderr %q", errOut)
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/control/ -run 'TestBuildLogFollow' -count=1`
Expected: build failure, `unknown field Done` / `undefined: followSettle`.

- [ ] **Step 3: Implement**

`internal/control/control.go`, in `Ctx` after `Cmd`:

```go
	// Done, when the surface has one, closes when nobody is reading any
	// more: the SSH channel closed or the HTTP request ended. A command
	// that runs until something happens (build log --follow) stops on it.
	Done <-chan struct{}
```

`internal/control/build.go` registration:

```go
	register(Command{Path: []string{"build", "log"},
		Summary: "print a build's log, or follow it until the build ends",
		Usage:   "build log <owner/name> <n> [--follow]", ReadOnly: true, Run: runBuildLog})
```

`runBuildLog`:

```go
func runBuildLog(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Bools: []string{"--follow"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	_, b, code := buildRef(c, f.Pos)
	if code >= 0 {
		return code
	}
	if f.Has("--follow") {
		return followBuildLog(c, b)
	}
	log, err := c.Store.BuildLog(b.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Stdout.Write(log)
	return protocol.ExitOK
}
```

Check how `runBuildList` (build.go ~125) reports a `parseFlags` error and match it exactly if it differs from the above.

`internal/control/buildfollow.go`:

```go
package control

import (
	"fmt"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// maxFollows is how many build log follows one account holds open at
// once. Signed-out web viewers are account 0 and share it.
const maxFollows = 8

var (
	// followPoll bounds a wait with no wake. A write from another process
	// (gitbayd admin, or any session under gitbayd shell) wakes nobody;
	// this is how its bytes still arrive.
	followPoll = 2 * time.Second
	// followSettle is how long a follow keeps reading after the build has
	// an outcome: a cancel appends its line after the status changes, and
	// a cancelled runner's stream runs on until its next check.
	followSettle = time.Second
)

var (
	followMu sync.Mutex
	follows  = map[int64]int{}
)

func takeFollow(uid int64) bool {
	followMu.Lock()
	defer followMu.Unlock()
	if follows[uid] >= maxFollows {
		return false
	}
	follows[uid]++
	return true
}

func dropFollow(uid int64) {
	followMu.Lock()
	defer followMu.Unlock()
	if follows[uid]--; follows[uid] <= 0 {
		delete(follows, uid)
	}
}

// followBuildLog writes the build's log as it grows and returns once the
// build has an outcome and its last bytes are written. The outcome goes
// to stderr, so stdout is the log byte for byte.
func followBuildLog(c *Ctx, b store.Build) int {
	if !takeFollow(c.User.ID) {
		return c.fail(protocol.ExitDenied, "%d follows are already open for this account; close one and retry", maxFollows)
	}
	defer dropFollow(c.User.ID)

	var off int64
	var settleBy time.Time
	for {
		wake := c.Store.BuildLogWait(b.ID)
		status, chunk, err := c.Store.BuildLogFrom(b.ID, off)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if len(chunk) > 0 {
			if _, err := c.Stdout.Write(chunk); err != nil {
				return protocol.ExitFailure
			}
			off += int64(len(chunk))
		}
		wait := followPoll
		if status != "pending" && status != "running" {
			if settleBy.IsZero() {
				settleBy = time.Now().Add(followSettle)
			}
			left := time.Until(settleBy)
			if left <= 0 && len(chunk) == 0 {
				fmt.Fprintf(c.Stderr, "build %d %s\n", b.Number, status)
				return protocol.ExitOK
			}
			wait = min(wait, max(left, 0))
		}
		t := time.NewTimer(wait)
		select {
		case <-wake:
		case <-t.C:
		case <-c.Done:
			t.Stop()
			return protocol.ExitFailure
		}
		t.Stop()
	}
}
```

Check the loop against the spec before moving on: once the status is terminal it keeps reading until `followSettle` has passed *and* a read came back empty, then prints the outcome. A deadline, not a `time.After` channel: a timer channel delivers once, and a second check of it would block. A nil `c.Done` never fires in the select, which is what a surface without one wants. `min`/`max` are Go 1.21 builtins; check `go.mod`'s go line is at least 1.21.

`cmd/gitbay/main.go:51`:

```go
			pass("log", "a build's log: <owner/name> <n> [--follow]", passOpts{server: []string{"build", "log"}, needsRepo: true}),
```

- [ ] **Step 4: Run the tests, the race detector, and vet**

Run: `go test ./internal/control/ -run 'TestBuildLog' -count=1 -race && go vet ./internal/control/ ./cmd/gitbay/ && go test ./cmd/gitbay/ -count=1`
Expected: `ok` for each.

Then run the whole control package once, since `build log` is covered elsewhere too: `go test ./internal/control/ -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/control/control.go internal/control/build.go internal/control/buildfollow.go internal/control/buildfollow_test.go cmd/gitbay/main.go
git commit -m "build log --follow: stream a build's log until it ends

Ref #250"
```

---

### Task 3: Surfaces pass Done — SSH channel close, HTTP request end

**Files:**
- Modify: `internal/sshd/sshd.go` (`handleSession` ~230, `runExec` ~270, `Exec` ~305, the `control.Ctx` at ~335)
- Modify: `cmd/gitbayd/system.go:96`
- Modify: `internal/httpd/api.go` (~64), `internal/httpd/apiread.go` (~53)

**Interfaces:**
- Consumes: `Ctx.Done` (Task 2).
- Produces: `sshd.Exec(cfg, st, user, scope, source, cmdline string, stdin io.Reader, stdout, stderr io.Writer, done <-chan struct{}) int`.

- [ ] **Step 1: SSH.** In `handleSession`'s `"exec"` case, replace the two lines after `req.Reply(true, nil)`:

```go
			req.Reply(true, nil)
			// x/crypto closes reqs when the client closes the channel. That
			// is how a follow learns nobody is reading: the CLI's shared
			// connection outlives a Ctrl-C, the channel does not.
			done := make(chan struct{})
			go func() {
				for r := range reqs {
					r.Reply(false, nil)
				}
				close(done)
			}()
			code := s.runExec(sconn, ch, payload.Command, done)
			sendExit(ch, code)
			return
```

`runExec` gains `done <-chan struct{}` as its last parameter and passes it to `Exec`. `Exec` gains `done <-chan struct{}` as its last parameter and sets `Done: done` in the `control.Ctx` it builds. `cmd/gitbayd/system.go:96` passes `nil` (that process ends with its session).

Find every other caller: `grep -rn 'sshd.Exec(\|\.runExec(' --include='*.go' .` and update them, test files included.

- [ ] **Step 2: API.** In `internal/httpd/api.go` and `internal/httpd/apiread.go`, add `Done: r.Context().Done(),` to the `control.Ctx` literal.

- [ ] **Step 3: Build, vet, test**

Run: `go build ./... && go vet ./internal/sshd/ ./internal/httpd/ ./cmd/gitbayd/ && go test ./internal/sshd/ ./internal/httpd/ -count=1`
Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
git add internal/sshd/sshd.go cmd/gitbayd/system.go internal/httpd/api.go internal/httpd/apiread.go
git commit -m "sshd, api: end a command when its reader goes away

Ref #250"
```

---

### Task 4: gzipWriter passes a flush through

**Files:**
- Modify: `internal/httpd/compress.go`
- Test: `internal/httpd/compress_test.go`

**Interfaces:**
- Produces: `(*gzipWriter).Flush()`, `(*gzipWriter).Unwrap() http.ResponseWriter`.

- [ ] **Step 1: Write the failing test** — append to `internal/httpd/compress_test.go` (add missing imports: `bytes`, `compress/gzip`, `io`, `net/http`, `net/http/httptest`, `strings`):

```go
// A flush mid-response reaches the connection with what was written so
// far decodable, which is what lets a page stream through gzip.
func TestGzipWriterFlushes(t *testing.T) {
	rec := httptest.NewRecorder()
	h := compressed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<p>first</p>")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		if !rec.Flushed {
			t.Fatal("the flush did not reach the connection")
		}
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("gzip header: %v", err)
		}
		got, _ := io.ReadAll(zr) // no trailer yet: ends in ErrUnexpectedEOF
		if !strings.Contains(string(got), "<p>first</p>") {
			t.Fatalf("flushed body decodes to %q", got)
		}
		io.WriteString(w, "<p>second</p>")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
}
```

- [ ] **Step 2: Run to see it fail**

Run: `go test ./internal/httpd/ -run TestGzipWriterFlushes -count=1`
Expected: FAIL, `flush: feature not supported`.

- [ ] **Step 3: Implement** — in `internal/httpd/compress.go`, after `Close`:

```go
// Flush sends what the gzip stream holds, then flushes the connection, so
// a streamed page reaches the browser as it is written.
func (g *gzipWriter) Flush() {
	if !g.decided {
		g.decide(http.StatusOK)
	}
	if g.gz != nil {
		g.gz.Flush()
	}
	http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
```

- [ ] **Step 4: Run**

Run: `go test ./internal/httpd/ -count=1 && go vet ./internal/httpd/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/httpd/compress.go internal/httpd/compress_test.go
git commit -m "httpd: gzipWriter passes a flush through

Ref #250"
```

---

### Task 5: The build page streams a live build

**Files:**
- Modify: `internal/httpd/builds.go` (`build`, ~280)
- Modify: `internal/httpd/control.go` (new `runControlStream` after `runControlCode`)
- Modify: `internal/web/templates/build.html`

**Interfaces:**
- Consumes: `build log --follow` (Task 2), `gzipWriter.Flush` (Task 4), `Ctx.Done`.
- Produces: `func (s *Server) runControlStream(u store.User, argv []string, out io.Writer, done <-chan struct{}) (msg string, code int)`; `type buildView`; `const liveLogMarker`.

- [ ] **Step 1: Template.** Replace the last content line of `build.html`:

```
{{if .Log}}<pre class="code buildlog" tabindex="0">{{.Log}}</pre>{{else}}<p class="empty-note">no log yet</p>{{end}}
```

with:

```
{{if .Live}}<p class="meta">Live: the log streams here until the build ends. If it stops without a “build finished” line, reload to pick it up again. <a href="?follow=0">Show it without updates</a></p>
<pre class="code buildlog" tabindex="0">{{.Log}}</pre>
{{else if .Log}}<pre class="code buildlog" tabindex="0">{{.Log}}</pre>{{else}}<p class="empty-note">no log yet</p>{{end}}
```

- [ ] **Step 2: `runControlStream`** in `internal/httpd/control.go` after `runControlCode` (add `io` to imports):

```go
// runControlStream runs a command whose output is written as it is
// produced: stdout goes to out, and done ends the command when the
// request does. msg is stderr.
func (s *Server) runControlStream(u store.User, argv []string, out io.Writer, done <-chan struct{}) (msg string, code int) {
	var stderr bytes.Buffer
	ctx := &control.Ctx{
		User:   u,
		Source: "web",
		Scope:  "full",
		Store:  s.st,
		Cfg:    s.cfg,
		Stdin:  strings.NewReader(""),
		Stdout: out,
		Stderr: &stderr,
		ViaAPI: true,
		Done:   done,
	}
	code = control.Dispatch(ctx, argv)
	return strings.TrimSpace(stderr.String()), code
}
```

- [ ] **Step 3: Handler.** In `internal/httpd/builds.go`, replace `build` from the `log, _, _ := s.runControl(...)` line to the end with:

```go
	v := buildView{repoPage: p, Build: b, CanWrite: s.canWriteRepo(r, p.Repo), Notice: s.takeFlash(w, r)}
	if (b.Status == "pending" || b.Status == "running") && r.URL.Query().Get("follow") != "0" {
		s.streamBuild(w, r, v, viewer, n)
		return
	}
	v.Log, _, _ = s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})
	s.render(w, "build.html", v)
}

type buildView struct {
	repoPage
	Build    control.BuildOut
	Log      string
	Live     bool
	CanWrite bool
	Notice   string
}

// liveLogMarker stands in for the log when build.html is rendered for a
// live build; streamBuild splits the page there and streams the log into
// the gap. Git refs, paths and job names cannot hold the control byte.
const liveLogMarker = "\x1elive-log\x1e"

// streamBuild writes the build page with the log following the build:
// the page up to the log, then build log --follow escaped and flushed as
// it arrives, then the outcome and the rest of the page.
func (s *Server) streamBuild(w http.ResponseWriter, r *http.Request, v buildView, viewer store.User, n string) {
	v.Live, v.Log = true, liveLogMarker
	var buf bytes.Buffer
	if err := web.Render(&buf, "build.html", v); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	head, tail, ok := strings.Cut(buf.String(), liveLogMarker)
	if !ok || !strings.HasPrefix(tail, "</pre>") {
		http.Error(w, "template error: build.html has no live log slot", http.StatusInternalServerError)
		return
	}
	tail = strings.TrimPrefix(tail, "</pre>")

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	io.WriteString(w, head)
	rc.Flush()

	path := v.Repo.Path()
	msg, code := s.runControlStream(viewer, []string{"build", "log", path, n, "--follow"},
		htmlStream{w: w, rc: rc}, r.Context().Done())
	if code == protocol.ExitDenied {
		// The follow cap: the stored log once, and why it is not live.
		log, _, _ := s.runControl(viewer, []string{"build", "log", path, n})
		template.HTMLEscape(w, []byte(log))
	}
	io.WriteString(w, "</pre>")
	switch {
	case code == protocol.ExitOK:
		var b control.BuildOut
		if _, ok := s.runControlInto(viewer, []string{"build", "show", path, n}, &b); ok {
			fmt.Fprintf(w, `<p class="notice" role="status">build finished: %s</p>`, template.HTMLEscapeString(b.Status))
		}
	case code == protocol.ExitDenied:
		fmt.Fprintf(w, `<p class="error" role="alert">%s</p>`, template.HTMLEscapeString(msg))
	}
	io.WriteString(w, tail)
}

// htmlStream escapes each chunk of a streamed log into the page and
// flushes it, so the browser draws it as it arrives.
type htmlStream struct {
	w  io.Writer
	rc *http.ResponseController
}

func (h htmlStream) Write(p []byte) (int, error) {
	template.HTMLEscape(h.w, p)
	if err := h.rc.Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}
```

Add the imports `builds.go` now needs (`bytes`, `fmt`, `html/template`, `io`, `strings`, `gitbay.org/gitbay/internal/protocol`, `gitbay.org/gitbay/internal/web`) — only those not already present. If `builds.go` already imports `text/template` or another `template`, alias accordingly.

- [ ] **Step 4: Build, vet, unit tests**

Run: `go build ./... && go vet ./internal/httpd/ ./internal/web/ && go test ./internal/httpd/ ./internal/web/ -count=1`
Expected: `ok`. (`TestMainWidthClass` needs nothing: no new template.)

- [ ] **Step 5: Commit**

```bash
git add internal/httpd/builds.go internal/httpd/control.go internal/web/templates/build.html
git commit -m "web: the build page streams a live build's log

Ref #250"
```

---

### Task 6: e2e — follow over ssh and on the page

**Files:**
- Modify: `e2e/ssh_test.go` (extract `sshCmd` from `ssh`, ~line 164)
- Create: `e2e/buildfollow_test.go`

**Interfaces:**
- Consumes: everything above; e2e helpers `startInstance`, `newKey`, `admin`, `ssh`, `gitEnv`, `sshURL`, `mustGit`, `httpPort`.
- Produces: `func (i *instance) sshCmd(key string, args ...string) *exec.Cmd`.

- [ ] **Step 1: Extract `sshCmd`.** In `e2e/ssh_test.go`, split `ssh`:

```go
// sshCmd is the ssh invocation ssh runs, for a test that reads the output
// as it arrives.
func (i *instance) sshCmd(key string, args ...string) *exec.Cmd {
	base := []string{
		"-p", fmt.Sprint(i.port),
		"-i", key,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(i.sshDir, "known_hosts"),
		"-o", "BatchMode=yes",
		"git@127.0.0.1",
	}
	return exec.Command("ssh", append(base, args...)...)
}
```

and have `ssh` start with `cmd := i.sshCmd(key, args...)` in place of building `base` itself.

- [ ] **Step 2: Write the test** — `e2e/buildfollow_test.go`:

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// streamReader collects what r delivers, so a test can wait for text to
// arrive while the writer is still going.
type streamReader struct {
	ch  chan []byte
	buf strings.Builder
}

func newStreamReader(r io.Reader) *streamReader {
	s := &streamReader{ch: make(chan []byte, 16)}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			if n > 0 {
				s.ch <- append([]byte(nil), b[:n]...)
			}
			if err != nil {
				close(s.ch)
				return
			}
		}
	}()
	return s
}

func (s *streamReader) waitFor(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(s.buf.String(), want) {
		select {
		case b, ok := <-s.ch:
			if !ok {
				t.Fatalf("stream ended before %q:\n%s", want, s.buf.String())
			}
			s.buf.Write(b)
		case <-deadline:
			t.Fatalf("no %q after 20s:\n%s", want, s.buf.String())
		}
	}
	return s.buf.String()
}

// A running build is followed over ssh and on its page: output the runner
// sends arrives while the build runs, and both end with the outcome.
func TestBuildLogFollow(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Claim build 1 by hand, so the test decides when output arrives.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 {
		t.Fatalf("runner next: %s", errOut)
	}
	var claim struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &claim); err != nil || claim.Data.ID == 0 {
		t.Fatalf("runner next output %q: %v", out, err)
	}
	id := fmt.Sprint(claim.Data.ID)

	cmd := inst.sshCmd(aliceKey, "build", "log", "alice/app", "1", "--follow")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	follow := newStreamReader(stdout)

	page, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/builds/1", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	web := newStreamReader(page.Body)
	web.waitFor(t, "Live: the log streams here")

	// A static render while the build runs returns at once.
	static := &http.Client{Timeout: 10 * time.Second}
	resp, err := static.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/app/builds/1?follow=0", inst.httpPort))
	if err != nil {
		t.Fatalf("?follow=0 did not return: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "Live:") {
		t.Fatalf("?follow=0 rendered the live page:\n%s", body)
	}

	if _, errOut, code := inst.ssh(t, runnerKey, "hello from the runner <b>\n", "runner", "log", id); code != 0 {
		t.Fatalf("runner log: %s", errOut)
	}
	follow.waitFor(t, "hello from the runner <b>\n")
	web.waitFor(t, "hello from the runner &lt;b&gt;")

	if _, errOut, code := inst.ssh(t, runnerKey, "", "runner", "done", id, "success"); code != 0 {
		t.Fatalf("runner done: %s", errOut)
	}
	web.waitFor(t, `<p class="notice" role="status">build finished: success</p>`)
	web.waitFor(t, "</html>")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("follow exited: %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "build 1 success" {
		t.Errorf("follow stderr %q", got)
	}
}
```

- [ ] **Step 3: Run it**

Run: `go test ./e2e/ -run 'TestBuildLogFollow$' -count=1 -v 2>&1 | tail -20`
Expected: `--- PASS: TestBuildLogFollow`. If `</html>` is not how the layout ends, use the last line `layout.html` renders.

- [ ] **Step 4: Check the neighbours.** Run `go vet ./e2e/` and the tests that use `inst.ssh` heavily and build pages: `go test ./e2e/ -run 'TestBuildCancel$|TestControlPlaneOverBareSSH$' -count=1`.

- [ ] **Step 5: Commit**

```bash
git add e2e/ssh_test.go e2e/buildfollow_test.go
git commit -m "e2e: follow a running build over ssh and on its page

Ref #250"
```

---

### Task 7: Docs

**Files:**
- Modify: `.gitbay/wiki/Parity.org` (build rows, ~line 212)
- Modify: `.gitbay/wiki/CI.org`

- [ ] **Step 1: Parity.** After `| build log                   | yes | yes | yes |` add (columns are cli, web, ios):

```
| build log follow (until it ends) | yes | yes | no  |
```

- [ ] **Step 2: CI.org.** After the paragraph that begins "Scheduled jobs run on their cron", add:

```
A running build is followed with =build log <owner/name> <n> --follow=:
the stored log, then output as the runner sends it, then the outcome
as =build <n> <status>= on stderr once the build ends. The exit code is
0 whatever the outcome. The build page does the same without
JavaScript while a build is queued or running; =?follow=0= renders it
once. An account holds at most eight follows open, and signed-out
viewers share one account's eight. Over the JSON API the command
answers when the build ends, with the whole log.
```

- [ ] **Step 3: Commit**

```bash
git add .gitbay/wiki/Parity.org .gitbay/wiki/CI.org
git commit -m "wiki: build log --follow

Closes #250"
```

---

## Finish

Push `build-log-follow`, open the MR with `gitbay mr create --source build-log-follow --target main --title "build log --follow, streamed to the build page" --file - < <body file>`, wait for CI, then `gitbay mr merge <n> --strategy ff` and delete the branch in both places.
