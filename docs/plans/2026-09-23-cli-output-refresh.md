# CLI output refresh implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Terminal rendering for tables, `show` views and help, selected
by the client, with piped output unchanged in shape, plus the audit
fixes from 2026-09-23.

**Architecture:** The server renders. The CLI sends
`GITBAY_TERM=<cols>[,color]` on the SSH session; sshd parses it into
`Ctx.Term`; a `table` helper, a `view` helper and an
`internal/termtext` renderer pick terminal or plain output from it.
The CLI adds a pager and a grouped root help. Help text (flag
descriptions, examples) moves into the command registry.

**Tech stack:** Go, `golang.org/x/crypto/ssh`, goldmark, go-org,
chroma v2, `golang.org/x/text/width`, `golang.org/x/term`, cobra.

**Spec:** `docs/specs/2026-09-23-cli-output-refresh-design.md`

## Global constraints

- Five MRs, in order, each on its own branch off `main`:
  `cli-output-refresh` (Part 1, already holds the spec and this
  plan), `cli-output-tables` (Part 2), `cli-output-views` (Part 3),
  `cli-output-help` (Part 4), `cli-output-docs` (Part 5).
- Commits are signed (the repository refuses unsigned ones), end with
  `Ref #254`; the last commit of Part 5 says `Closes #254`. No
  attribution to any assistant or model anywhere.
- MR: `gitbay mr create --source <branch> --target main --title "..."`;
  merge with `gitbay mr merge <n> --strategy ff` after CI is green,
  then delete the branch locally and on the remote. If the merge says
  the branch is behind, rebase onto `main`, force-push, merge again.
- Locally run `go build ./...`, `go vet ./...` (catches test callers
  after a signature change), and the unit tests of touched packages.
  Run at most the one e2e test being written:
  `go test ./e2e -run TestName -count=1`. CI on bay1 runs the rest.
- `--json` output never changes shape except where a task says so
  (Part 1 `release list` paging, Part 4 `help --json` gaining two
  fields).
- Piped (plain) output keeps its row shape: tab-separated, no header.
  Timestamps in plain output become RFC3339 to the second in UTC
  (`2026-09-23T23:26:00Z`).
- No ANSI byte (`\x1b`) ever reaches plain output.
- Terminal timestamps: `2006-01-02 15:04 UTC` in views, relative ages
  in tables (`just now`, `5m ago`, `2h ago`, `3d ago` under 14 days,
  `3w ago` under 8 weeks, then `2006-01-02`).
- Colours are ANSI 16-colour: green (`--ok`), magenta (`--done`), red
  (`--bad`), dim (`--neutral`).
- Comments and docs: plain, terse, no before/after commentary.

## File map

| File | Part | Responsibility |
|---|---|---|
| `internal/store/releases.go` | 1 | `ListReleasesPage` |
| `internal/control/release.go` | 1 | `release list` paging, empty title |
| `internal/control/notifications.go` | 1 | device add message |
| `internal/control/dashboard.go` | 1, 2 | `none` under empty sections; tables |
| `internal/control/term.go` | 2 | `Term`, `ParseTerm`, ANSI, cell width, clip, timestamps |
| `internal/control/table.go` | 2 | `table`, typed cells |
| `internal/control/control.go` | 2, 4 | `Ctx.Term`, `Ctx.Argv`, `Command.Flags/Examples`, help |
| `internal/control/cursor.go` | 2 | `more:` hint in terminal mode |
| `internal/sshd/sshd.go` | 2 | `env` request, `Exec` takes a `Term` |
| `cmd/gitbayd/system.go` | 2 | forced command reads `GITBAY_TERM` |
| `cmd/gitbay/ssh.go` | 2, 3 | `SetEnv`, `--no-color`, pager; `alignColumns` removed |
| `internal/control/*.go` list sites | 2 | every list command on `table` |
| `internal/termtext/` | 3 | markdown and org to terminal text |
| `internal/control/view.go` | 3 | layout of every `show` |
| `internal/control/help.go` | 4 | help rendering, noun summaries |
| `cmd/gitbay/main.go` | 4 | summaries from the generated table, grouped root help |
| `cmd/gitbay/summaries_gen.go` | 4 | generated from the registry |
| `e2e/readonly_test.go`, `e2e/ssh_test.go`, `e2e/term_test.go` | 2, 3 | terminal and plain checks over real ssh |
| `.gitbay/wiki/Users.org`, `Admin.org`, `CHANGELOG.org` | 5 | rules, operator note, release note |

---

# Part 1: audit fixes (branch `cli-output-refresh`)

### Task 1.1: `release list` pages and drops a title equal to its tag

**Files:**
- Modify: `internal/store/releases.go` (`ListReleases`, around line 92)
- Modify: `internal/control/release.go:29-31` (registration), `:199-220` (`runReleaseList`)
- Modify: `cmd/gitbay/main.go` (the `release list` `pass()` short text)
- Test: `internal/control/release_test.go` (create)

**Interfaces:**
- Produces: `func (s *Store) ListReleasesPage(repoID int64, limit int, afterID int64) ([]Release, error)` — newest first by `(created_at, id)`; `limit` 0 means all; `afterID` 0 means from the start.

- [ ] **Step 1: Write the failing test**

```go
package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestReleaseListPagesAndHidesTagTitle(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, tag := range []string{"v1", "v2", "v3"} {
		title := tag
		if tag == "v2" {
			title = "Second"
		}
		if _, err := st.CreateRelease(repo.ID, tag, title, "", uid, "md"); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(c.Stdout.(*bytes.Buffer).String()), "\n")
	if len(lines) != 3 || lines[0] != "v3\t\t0 asset(s)" || lines[1] != "v2\tSecond\t0 asset(s)" || !strings.HasPrefix(lines[2], "next\t") {
		t.Fatalf("page 1:\n%s", strings.Join(lines, "\n"))
	}
	cursor := strings.TrimPrefix(lines[2], "next\t")

	c, errOut = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"release", "list", repo.Path(), "--cursor", cursor}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := c.Stdout.(*bytes.Buffer).String(); got != "v1\t\t0 asset(s)\n" {
		t.Fatalf("page 2: %q", got)
	}
}
```

The three releases can share a `created_at` second; the `(created_at, id)` order is what keeps the pages stable.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestReleaseListPagesAndHidesTagTitle -count=1`
Expected: FAIL, exit 2 (`--limit` is not accepted).

- [ ] **Step 3: Store**

Replace `ListReleases` in `internal/store/releases.go` with:

```go
func (s *Store) ListReleases(repoID int64) ([]Release, error) {
	return s.ListReleasesPage(repoID, 0, 0)
}

// ListReleasesPage lists newest first. limit 0 is every row; afterID is
// the last release of the previous page, 0 for the first.
func (s *Store) ListReleasesPage(repoID int64, limit int, afterID int64) ([]Release, error) {
	q := releaseSelect + " WHERE r.repo_id = ?"
	args := []any{repoID}
	if afterID > 0 {
		q += " AND (r.created_at, r.id) < (SELECT created_at, id FROM releases WHERE id = ?)"
		args = append(args, afterID)
	}
	q += " ORDER BY r.created_at DESC, r.id DESC"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.RepoID, &r.Tag, &r.Title, &r.Notes, &r.NotesFormat, &r.Author, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.releaseAssets(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Command**

Registration usage becomes `release list <owner/name> [--limit <n>] [--cursor <c>]`. `runReleaseList`:

```go
func runReleaseList(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "release", true)
	if code >= 0 {
		return code
	}
	if len(rest) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, rest[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	rels, err := c.Store.ListReleasesPage(repo.ID, p.queryLimit(), p.keyInt())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	rels, next := trimPage(p, rels, "release", func(r store.Release) string { return strconv.FormatInt(r.ID, 10) })
	var ds []releaseOut
	for _, r := range rels {
		ds = append(ds, releaseToOut(r, false))
	}
	return c.emitPage(p, ds, next, func(w io.Writer) {
		for _, d := range ds {
			title := d.Title
			if title == d.Tag {
				title = ""
			}
			fmt.Fprintf(w, "%s\t%s\t%d asset(s)\n", d.Tag, title, len(d.Assets))
		}
	})
}
```

Add `strconv` to the imports if missing. In `cmd/gitbay/main.go` the `release list` short text becomes `"releases: <owner/name> [--limit <n>] [--cursor <c>]"` (Part 4 removes these strings).

- [ ] **Step 5: Run the test and the package**

Run: `go test ./internal/control ./internal/store -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/releases.go internal/control/release.go internal/control/release_test.go cmd/gitbay/main.go
git commit -m "release list: --limit and --cursor; no title column when it repeats the tag" -m "Ref #254"
```

### Task 1.2: `notifications device add` says `registered device <n>`

**Files:**
- Modify: `internal/control/notifications.go:282`
- Test: `internal/control/notifications_test.go`

- [ ] **Step 1: Find the existing device add test**

Run: `grep -n 'registered' internal/control/notifications_test.go e2e/*.go`
Every assertion on `device %d registered` changes with the message. If none asserts on the plain text, add to the existing device add test in `notifications_test.go`, after its successful `Dispatch` (the test runs with `c.JSON` false or add a second plain run):

```go
if got := c.Stdout.(*bytes.Buffer).String(); !strings.HasPrefix(got, "registered device ") {
	t.Errorf("device add printed %q", got)
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run Device -count=1`
Expected: FAIL on the message.

- [ ] **Step 3: Change the message**

```go
fmt.Fprintf(w, "registered device %d\n", id)
```

Update any e2e assertion found in Step 1 the same way.

- [ ] **Step 4: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/notifications.go internal/control/notifications_test.go
git commit -m "notifications device add: verb first" -m "Ref #254"
```

### Task 1.3: `dashboard` prints `none` under an empty section

**Files:**
- Modify: `internal/control/dashboard.go:165-210` and `printDashboardItems`
- Test: `internal/control/dashboard_test.go`

- [ ] **Step 1: Write the failing test**

Add to `dashboard_test.go`, using the store/ctx setup the file's existing tests use (read the top of the file for its helper):

```go
func TestDashboardEmptySectionsSayNone(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"dashboard"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()
	for _, h := range []string{"waiting on your review:", "assigned to you:", "open merge requests:", "open issues:"} {
		if !strings.Contains(out, h+"\n  none\n") {
			t.Errorf("%q not followed by none:\n%s", h, out)
		}
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestDashboardEmptySectionsSayNone -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `printDashboardItems`, before its loop:

```go
if len(items) == 0 {
	fmt.Fprintln(w, "  none")
	return
}
```

(Use the function's own parameter name.) Apply the same guard to the `pinned`, `recent activity` and `builds` loops in the plain formatter:

```go
fmt.Fprintln(w, "pinned:")
if len(d.Pinned) == 0 {
	fmt.Fprintln(w, "  none")
}
for _, p := range d.Pinned {
```

and likewise for `d.Activity` and `d.Builds`.

- [ ] **Step 4: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS. Fix any existing dashboard assertion that expected a header followed directly by the next header.

- [ ] **Step 5: Commit and open MR 1**

```bash
git add internal/control/dashboard.go internal/control/dashboard_test.go
git commit -m "dashboard: none under an empty section" -m "Ref #254"
git push -u origin cli-output-refresh
gitbay mr create --source cli-output-refresh --target main --title "CLI output refresh: spec, plan, audit fixes"
```

Wait for CI, merge (`--strategy ff`), delete the branch both places.

---

# Part 2: transport and tables (branch `cli-output-tables`)

Start: `git switch main && git pull --ff-only && git switch -c cli-output-tables`.

### Task 2.1: `Term`, cell width, clipping, timestamps

**Files:**
- Create: `internal/control/term.go`
- Test: `internal/control/term_test.go`
- Modify: `go.mod` (`golang.org/x/text` moves from indirect to direct: `go mod tidy`)

**Interfaces:**
- Produces:
  - `type Term struct { Cols int; Color bool }` — zero value is plain.
  - `func ParseTerm(v string) Term`
  - `func (t Term) paint(sgr, s string) string`
  - `func stateColor(s string) string` — an SGR prefix or `""`.
  - `func cells(s string) int` — display width, SGR sequences skipped.
  - `func runeCells(r rune) int`
  - `func clip(s string, w int) string`
  - `func pad(s string, w int) string`
  - `func parseStamp(s string) (time.Time, bool)`
  - `func stamp(s string) string` — plain timestamp.
  - `func relAge(s string, now time.Time) string`
  - `var termNow = time.Now`
  - constants `sgrReset sgrBold sgrDim sgrUnderline sgrRed sgrGreen sgrMagenta`

- [ ] **Step 1: Write the failing tests**

```go
package control

import (
	"testing"
	"time"
)

func TestParseTerm(t *testing.T) {
	cases := map[string]Term{
		"120":       {Cols: 120},
		"120,color": {Cols: 120, Color: true},
		"40":        {Cols: 40},
		"39":        {},
		"":          {},
		"abc":       {},
		"80,blink":  {},
		"80,":       {},
		"5000":      {},
	}
	for in, want := range cases {
		if got := ParseTerm(in); got != want {
			t.Errorf("ParseTerm(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCells(t *testing.T) {
	cases := map[string]int{
		"abc":                   3,
		"日本":                    4,
		"é":                     1,
		"é":               1,
		"\x1b[32mopen\x1b[0m":   4,
		"":                      0,
	}
	for in, want := range cases {
		if got := cells(in); got != want {
			t.Errorf("cells(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("Dependency updates available", 14); got != "Dependency up…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 14); got != "short" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("日本語のタイトル", 7); got != "日本語…" {
		t.Errorf("clip wide = %q", got)
	}
}

func TestStampAndRelAge(t *testing.T) {
	if got := stamp("2026-09-23T23:26:00.570Z"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp = %q", got)
	}
	if got := stamp("2026-09-23 23:26:00"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp sqlite = %q", got)
	}
	if got := stamp("garbage"); got != "garbage" {
		t.Errorf("stamp garbage = %q", got)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"2026-09-23T11:59:30Z": "just now",
		"2026-09-23T11:55:00Z": "5m ago",
		"2026-09-23T10:00:00Z": "2h ago",
		"2026-09-20T12:00:00Z": "3d ago",
		"2026-09-02T12:00:00Z": "3w ago",
		"2026-06-01T12:00:00Z": "2026-06-01",
		"2026-09-24T12:00:00Z": "just now",
		"not a time":           "not a time",
	}
	for in, want := range cases {
		if got := relAge(in, now); got != want {
			t.Errorf("relAge(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run and see them fail**

Run: `go test ./internal/control -run 'TestParseTerm|TestCells|TestClip|TestStampAndRelAge' -count=1`
Expected: FAIL to compile (`undefined: ParseTerm`).

- [ ] **Step 3: Implement `internal/control/term.go`**

```go
package control

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// Term is what the client said about its terminal (GITBAY_TERM). The
// zero value is plain output: tab-separated rows, no header, no colour,
// which is what stock ssh, the API and the web get.
type Term struct {
	Cols  int
	Color bool
}

// ParseTerm reads "<cols>[,color]". Anything else, or a width outside
// 40 to 1000, is plain output.
func ParseTerm(v string) Term {
	cols, opt, hasOpt := strings.Cut(v, ",")
	n, err := strconv.Atoi(cols)
	if err != nil || n < 40 || n > 1000 {
		return Term{}
	}
	switch {
	case !hasOpt:
		return Term{Cols: n}
	case opt == "color":
		return Term{Cols: n, Color: true}
	}
	return Term{}
}

const (
	sgrReset     = "\x1b[0m"
	sgrBold      = "\x1b[1m"
	sgrDim       = "\x1b[2m"
	sgrUnderline = "\x1b[4m"
	sgrRed       = "\x1b[31m"
	sgrGreen     = "\x1b[32m"
	sgrMagenta   = "\x1b[35m"
)

// paint wraps s in an SGR sequence when colour is on.
func (t Term) paint(sgr, s string) string {
	if !t.Color || sgr == "" || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

// stateColor maps a state word to the web's state tokens: --ok green,
// --done magenta, --bad red, --neutral dim.
func stateColor(s string) string {
	switch s {
	case "open", "success", "approved", "active":
		return sgrGreen
	case "merged":
		return sgrMagenta
	case "failed", "failure", "error", "changes requested":
		return sgrRed
	case "closed", "draft", "pending", "canceled", "cancelled", "archived", "disabled":
		return sgrDim
	}
	return ""
}

// cells is the width of s in terminal cells: SGR sequences and
// combining marks take none, East Asian wide and fullwidth runes two.
func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n += runeCells(r)
	}
	return n
}

func runeCells(r rune) int {
	if unicode.In(r, unicode.Mn, unicode.Me) || r == '‍' {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// clip cuts s to at most w cells, ending in "…" when anything was cut.
// s must carry no SGR sequences: colour goes on after clipping.
func clip(s string, w int) string {
	if cells(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rc := runeCells(r)
		if used+rc > w-1 {
			break
		}
		b.WriteRune(r)
		used += rc
	}
	return b.String() + "…"
}

// pad right-pads s with spaces to w cells.
func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-cells(s)))
}

// termNow is the clock ages are measured against; tests pin it.
var termNow = time.Now

// parseStamp reads a stored timestamp: RFC3339 as the store writes it,
// or SQLite's datetime() form.
func parseStamp(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// stamp is a stored timestamp in plain output: RFC3339 to the second.
func stamp(s string) string {
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	return t.Format("2006-01-02T15:04:05Z")
}

// relAge is a stored timestamp as a table shows it at a terminal.
func relAge(s string, now time.Time) string {
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	d := max(now.Sub(t), 0)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case d < 56*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d/(7*24*time.Hour)))
	}
	return t.Format("2006-01-02")
}
```

Then `go mod tidy`.

- [ ] **Step 4: Run**

Run: `go test ./internal/control -run 'TestParseTerm|TestCells|TestClip|TestStampAndRelAge' -count=1`
Expected: PASS. If `日本語…` fails, check `clip`'s budget: `w-1` cells for runes plus one for `…`.

- [ ] **Step 5: Commit**

```bash
git add internal/control/term.go internal/control/term_test.go go.mod go.sum
git commit -m "control: Term, cell width, clipping and timestamp formats" -m "Ref #254"
```

### Task 2.2: `table`

**Files:**
- Create: `internal/control/table.go`
- Modify: `internal/control/control.go` (`Ctx` gains `Term Term`)
- Test: `internal/control/table_test.go`

**Interfaces:**
- Consumes: everything in Task 2.1.
- Produces:
  - `type cell struct { kind cellKind; s string }`
  - `func cRef(s string) cell`, `cState(s string) cell`, `cText(s string) cell`, `cFlex(s string) cell`, `cAge(ts string) cell`, `cNum(n int64) cell`
  - `func (c *Ctx) table(w io.Writer, header ...string) *table`
  - `func (t *table) row(cells ...cell)`
  - `func (t *table) flush()`
  - `func stripSGR(s string) string` (test helper exported to the package; used by e2e via its own copy)
  - `Ctx.Term Term`

`cFlex` marks the column that shrinks first (a title or description). `cText` columns shrink only after it, rightmost first. `cRef`, `cState`, `cAge`, `cNum` never shrink.

- [ ] **Step 1: Write the failing tests**

```go
package control

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func fixtureTable(c *Ctx, w *bytes.Buffer) {
	tb := c.table(w, "#", "STATE", "TITLE", "AUTHOR")
	tb.row(cRef("#252"), cState("open"), cFlex("Dependency updates available for every module"), cText("gitbay-bot"))
	tb.row(cRef("#12"), cState("closed"), cFlex("Android app"), cText("cmc"))
	tb.flush()
}

func TestTablePlainIsTabs(t *testing.T) {
	var b bytes.Buffer
	fixtureTable(&Ctx{}, &b)
	want := "#252\topen\tDependency updates available for every module\tgitbay-bot\n" +
		"#12\tclosed\tAndroid app\tcmc\n"
	if b.String() != want {
		t.Errorf("plain:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestTableTerminalFits(t *testing.T) {
	var b bytes.Buffer
	fixtureTable(&Ctx{Term: Term{Cols: 40}}, &b)
	want := "#     STATE   TITLE           AUTHOR\n" +
		"#252  open    Dependency up…  gitbay-bot\n" +
		"#12   closed  Android app     cmc\n"
	if b.String() != want {
		t.Errorf("terminal:\n%s\nwant\n%s", b.String(), want)
	}
}

func TestTableColourOnlyAddsSGR(t *testing.T) {
	var mono, colour bytes.Buffer
	fixtureTable(&Ctx{Term: Term{Cols: 40}}, &mono)
	fixtureTable(&Ctx{Term: Term{Cols: 40, Color: true}}, &colour)
	if !strings.Contains(colour.String(), sgrGreen+"open"+sgrReset) {
		t.Errorf("open not green: %q", colour.String())
	}
	if !strings.HasPrefix(colour.String(), sgrDim) {
		t.Errorf("header not dim: %q", colour.String())
	}
	if stripSGR(colour.String()) != mono.String() {
		t.Errorf("colour changed the layout:\n%s\nvs\n%s", stripSGR(colour.String()), mono.String())
	}
}

func TestTableAgesAndPlainStamps(t *testing.T) {
	termNow = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { termNow = time.Now })
	var plain, term bytes.Buffer
	for _, c := range []struct {
		ctx *Ctx
		w   *bytes.Buffer
	}{{&Ctx{}, &plain}, {&Ctx{Term: Term{Cols: 80}}, &term}} {
		tb := c.ctx.table(c.w, "#", "UPDATED")
		tb.row(cRef("#1"), cAge("2026-09-23T10:00:00.123Z"))
		tb.flush()
	}
	if plain.String() != "#1\t2026-09-23T10:00:00Z\n" {
		t.Errorf("plain = %q", plain.String())
	}
	if term.String() != "#   UPDATED\n#1  2h ago\n" {
		t.Errorf("term = %q", term.String())
	}
}

func TestTableEmptyPrintsNothing(t *testing.T) {
	var b bytes.Buffer
	(&Ctx{Term: Term{Cols: 80}}).table(&b, "#").flush()
	if b.Len() != 0 {
		t.Errorf("empty table printed %q", b.String())
	}
}
```

The empty case prints nothing because `emit` already said `nothing to list`
before the formatter runs.

- [ ] **Step 2: Run and see them fail**

Run: `go test ./internal/control -run TestTable -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Add `Term` to `Ctx`**

In `internal/control/control.go`, inside `type Ctx struct`, after `JSON bool`:

```go
	// Term is the client's terminal, from GITBAY_TERM. The zero value
	// is plain output.
	Term Term
```

- [ ] **Step 4: Implement `internal/control/table.go`**

```go
package control

import (
	"io"
	"strconv"
	"strings"
)

type cellKind int

const (
	kindText cellKind = iota
	kindFlex
	kindRef
	kindState
	kindAge
	kindNum
)

// cell is one column of a table row. The kind decides colour, time
// format, and whether the column may be clipped to fit the terminal.
type cell struct {
	kind cellKind
	s    string
}

func cRef(s string) cell   { return cell{kindRef, s} }
func cState(s string) cell { return cell{kindState, s} }
func cText(s string) cell  { return cell{kindText, s} }
func cFlex(s string) cell  { return cell{kindFlex, s} }
func cAge(ts string) cell  { return cell{kindAge, ts} }
func cNum(n int64) cell    { return cell{kindNum, strconv.FormatInt(n, 10)} }

// table is a list command's rows. Plain, each row is written as it
// comes, tab-separated with no header. At a terminal rows are held
// until flush, then written under a header, padded, and fitted to the
// width.
type table struct {
	term   Term
	w      io.Writer
	header []string
	rows   [][]cell
}

func (c *Ctx) table(w io.Writer, header ...string) *table {
	return &table{term: c.Term, w: w, header: header}
}

func (t *table) row(cs ...cell) {
	if t.term.Cols == 0 {
		parts := make([]string, len(cs))
		for i, c := range cs {
			if c.kind == kindAge {
				parts[i] = stamp(c.s)
			} else {
				parts[i] = c.s
			}
		}
		io.WriteString(t.w, strings.Join(parts, "\t")+"\n")
		return
	}
	now := termNow()
	for i := range cs {
		if cs[i].kind == kindAge {
			cs[i].s = relAge(cs[i].s, now)
		}
	}
	t.rows = append(t.rows, cs)
}

func (t *table) flush() {
	if t.term.Cols == 0 || len(t.rows) == 0 {
		return
	}
	n := len(t.header)
	widths := make([]int, n)
	for i, h := range t.header {
		widths[i] = cells(h)
	}
	for _, r := range t.rows {
		for i := 0; i < n && i < len(r); i++ {
			widths[i] = max(widths[i], cells(r[i].s))
		}
	}
	t.fit(widths)

	var b strings.Builder
	line := make([]string, n)
	for i, h := range t.header {
		line[i] = h
	}
	b.WriteString(t.term.paint(sgrDim, t.join(line, widths)) + "\n")
	for _, r := range t.rows {
		for i := 0; i < n; i++ {
			s := ""
			if i < len(r) {
				s = clip(r[i].s, widths[i])
			}
			line[i] = s
		}
		b.WriteString(t.joinRow(r, line, widths) + "\n")
	}
	io.WriteString(t.w, b.String())
}

// fit shrinks columns until a row fits the terminal: the flexible
// column first, down to 8 cells, then the other text columns from the
// right, down to 8 each.
func (t *table) fit(widths []int) {
	total := func() int {
		s := 2 * (len(widths) - 1)
		for _, w := range widths {
			s += w
		}
		return s
	}
	kinds := make([]cellKind, len(widths))
	if len(t.rows) > 0 {
		for i := range widths {
			if i < len(t.rows[0]) {
				kinds[i] = t.rows[0][i].kind
			}
		}
	}
	shrink := func(i int) {
		if over := total() - t.term.Cols; over > 0 && widths[i] > 8 {
			widths[i] = max(8, widths[i]-over)
		}
	}
	for i, k := range kinds {
		if k == kindFlex {
			shrink(i)
		}
	}
	for i := len(kinds) - 1; i >= 0; i-- {
		if kinds[i] == kindText {
			shrink(i)
		}
	}
}

// join pads every column but the last and separates them by two spaces.
func (t *table) join(line []string, widths []int) string {
	var b strings.Builder
	for i, s := range line {
		if i > 0 {
			b.WriteString("  ")
		}
		if i == len(line)-1 {
			b.WriteString(s)
		} else {
			b.WriteString(pad(s, widths[i]))
		}
	}
	return b.String()
}

// joinRow is join with state cells coloured after padding, so the
// SGR bytes never count against the width.
func (t *table) joinRow(r []cell, line []string, widths []int) string {
	var b strings.Builder
	for i, s := range line {
		if i > 0 {
			b.WriteString("  ")
		}
		padding := ""
		if i < len(line)-1 {
			padding = strings.Repeat(" ", max(0, widths[i]-cells(s)))
		}
		if i < len(r) && r[i].kind == kindState {
			s = t.term.paint(stateColor(s), s)
		}
		b.WriteString(s + padding)
	}
	return b.String()
}

// stripSGR removes SGR sequences, for tests and width checks.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```

The header line is painted dim as a whole, so `TestTableColourOnlyAddsSGR`'s prefix check holds.

- [ ] **Step 5: Run**

Run: `go test ./internal/control -run TestTable -count=1`
Expected: PASS. If `TestTableTerminalFits` fails, print widths: `#` 4, `STATE` 6, `TITLE` 45 shrinks by 31 to 14, `AUTHOR` 10; 4+6+14+10+6 = 40.

- [ ] **Step 6: Commit**

```bash
git add internal/control/table.go internal/control/table_test.go internal/control/control.go
git commit -m "control: table, plain rows or a fitted terminal table" -m "Ref #254"
```

### Task 2.3: `GITBAY_TERM` over SSH, and the multiplexing check

**Files:**
- Modify: `internal/sshd/sshd.go` (`handleSession`, `runExec`, `Exec`)
- Modify: `cmd/gitbayd/system.go:96`
- Modify: `internal/control/repo.go` (`repo list` plain formatter onto `table`, the first site, so the e2e test has a header to look for)
- Modify: `e2e/ssh_test.go` (add `sshTerm`)
- Create: `e2e/term_test.go`

**Interfaces:**
- Consumes: `control.ParseTerm`, `control.Term`, `Ctx.table`.
- Produces:
  - `func Exec(cfg config.Config, st *store.Store, user store.User, scope, source string, term control.Term, cmdline string, stdin io.Reader, stdout, stderr io.Writer, done, stopping <-chan struct{}) int`
  - e2e: `func (i *instance) sshTerm(t *testing.T, key, term string, args ...string) (string, string, int)` — `term` "" sends no `SetEnv`.

- [ ] **Step 1: Move `repo list` onto `table`**

Read `runRepoList` in `internal/control/repo.go`. Replace the `fmt.Fprintf` row loop in its plain formatter with a table. Columns: `PATH` (`cRef`), `VISIBILITY` (`cState`), `DESCRIPTION` (`cFlex`), keeping any trailing word column as `cText` in the same position it has today. Example shape (adapt the field names to the struct in the file):

```go
tb := c.table(w, "PATH", "VISIBILITY", "DESCRIPTION")
for _, r := range rows {
	tb.row(cRef(r.Path), cState(r.Visibility), cFlex(r.Description))
}
tb.flush()
```

Run `go test ./internal/control -count=1`: plain bytes are unchanged, so existing tests pass.

- [ ] **Step 2: Write the failing e2e test**

`e2e/ssh_test.go`, next to `sshCmd`:

```go
// sshTerm is ssh with GITBAY_TERM set on the session, as the CLI sends
// it at a terminal. An empty term sends nothing.
func (i *instance) sshTerm(t *testing.T, key, term string, args ...string) (string, string, int) {
	t.Helper()
	cmd := i.sshCmd(key, args...)
	if term != "" {
		for j, a := range cmd.Args {
			if a == "git@127.0.0.1" {
				opt := []string{"-o", "SetEnv=GITBAY_TERM=" + term}
				cmd.Args = append(cmd.Args[:j:j], append(opt, cmd.Args[j:]...)...)
				break
			}
		}
	}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("ssh: %v", err)
	}
	return out.String(), errOut.String(), code
}
```

`e2e/term_test.go`:

```go
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GITBAY_TERM selects terminal output per session. Stock ssh without it
// gets the plain rows scripts read.
func TestTermEnvSelectsTerminalOutput(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %d %s", code, errOut)
	}

	plain, _, _ := inst.sshTerm(t, key, "", "repo", "list")
	if strings.Contains(plain, "PATH") || !strings.Contains(plain, "alice/app\t") {
		t.Errorf("plain repo list: %q", plain)
	}
	term, _, _ := inst.sshTerm(t, key, "80,color", "repo", "list")
	if !strings.HasPrefix(term, "\x1b[2mPATH") {
		t.Errorf("terminal repo list: %q", term)
	}
}

// The CLI shares one connection per instance. Each session's
// GITBAY_TERM must reach the server, not the one the master was opened
// with.
func TestTermEnvOverMultiplexedSession(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub",
		"--email", "alice@example.test", "--verified")
	inst.ssh(t, key, "", "repo", "create", "alice/app")

	dir, err := os.MkdirTemp("", "gbmux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "cm")
	mux := func(term string) string {
		t.Helper()
		cmd := inst.sshCmd(key, "repo", "list")
		opts := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + sock, "-o", "ControlPersist=30"}
		if term != "" {
			opts = append(opts, "-o", "SetEnv=GITBAY_TERM="+term)
		}
		for j, a := range cmd.Args {
			if a == "git@127.0.0.1" {
				cmd.Args = append(cmd.Args[:j:j], append(opts, cmd.Args[j:]...)...)
				break
			}
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("ssh %s: %v", term, err)
		}
		return string(out)
	}
	t.Cleanup(func() {
		exec.Command("ssh", "-o", "ControlPath="+sock, "-O", "exit", "git@127.0.0.1").Run()
	})

	if out := mux("80"); !strings.HasPrefix(out, "PATH") {
		t.Fatalf("master session: %q", out)
	}
	if out := mux("80,color"); !strings.HasPrefix(out, "\x1b[2mPATH") {
		t.Errorf("second session kept the master's GITBAY_TERM: %q", out)
	}
	if out := mux(""); strings.Contains(out, "PATH") {
		t.Errorf("session without GITBAY_TERM got terminal output: %q", out)
	}
}
```

- [ ] **Step 3: Run and see them fail**

Run: `go test ./e2e -run 'TestTermEnv' -count=1`
Expected: FAIL (the server ignores `env`).

- [ ] **Step 4: sshd**

In `handleSession`, declare `var term control.Term` before `for req := range reqs`, pass it to `runExec`, and split the `pty-req`/`env` case:

```go
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil && kv.Name == "GITBAY_TERM" {
				term = control.ParseTerm(kv.Value)
			}
			req.Reply(true, nil)
		case "pty-req":
			// Harmless; accept and ignore.
			req.Reply(true, nil)
```

`runExec(sconn, ch, term, payload.Command, done)`; `runExec` passes `term` to `Exec`; `Exec` gains `term control.Term` after `source` and sets `Term: term` in the `control.Ctx` literal (around line 364). In `cmd/gitbayd/system.go:96`:

```go
code := sshd.Exec(cfg, st, user, key.Scope, key.Fingerprint, control.ParseTerm(os.Getenv("GITBAY_TERM")), cmdline, os.Stdin, os.Stdout, os.Stderr, nil, nil)
```

(import `gitbay.org/gitbay/internal/control` there if it is not already.)

- [ ] **Step 5: Run**

Run: `go build ./... && go vet ./... && go test ./e2e -run 'TestTermEnv' -count=1`
Expected: `TestTermEnvSelectsTerminalOutput` PASS.

`TestTermEnvOverMultiplexedSession` decides the transport:
- PASS: keep `SetEnv`. Go to Step 7.
- FAIL on the second session: OpenSSH's mux client does not forward the new session's `SetEnv`. Do Step 6.

- [ ] **Step 6 (only if Step 5's mux test failed): `--term` argument**

In `Dispatch` (`internal/control/control.go`), in the loop that strips `--json`, also strip `--term=<v>`:

```go
	for _, a := range rest {
		if a == "--json" {
			c.JSON = true
			continue
		}
		if v, ok := strings.CutPrefix(a, "--term="); ok {
			c.Term = ParseTerm(v)
			continue
		}
		args = append(args, a)
	}
```

(match the loop's real shape). `Lookup` runs before the loop, so a leading `--term=` would break it: strip `--term=` from `argv` before `Lookup` too. Change both e2e tests to pass the term as a leading `--term=<v>` argument instead of `-o SetEnv=…`, keep the `env` handling in sshd (stock ssh users may still use it), and in Task 2.5 the CLI prepends `--term=<v>` to the server argv instead of adding `SetEnv`. Record the outcome in the spec's Transport section.

- [ ] **Step 7: Commit**

```bash
git add internal/sshd/sshd.go cmd/gitbayd/system.go internal/control/repo.go internal/control/control.go e2e/ssh_test.go e2e/term_test.go
git commit -m "sshd: GITBAY_TERM selects terminal output per session" -m "Ref #254"
```

### Task 2.4: `more:` hint, `Ctx.Argv`

**Files:**
- Modify: `internal/control/control.go` (`Ctx.Argv`, set in `Dispatch`)
- Modify: `internal/control/cursor.go` (`emitPage`)
- Test: `internal/control/cursor_test.go`

**Interfaces:**
- Produces: `Ctx.Argv []string` — the command's arguments after the path, with `--json` (and `--term=`) removed.

- [ ] **Step 1: Write the failing test**

```go
func TestEmitPageHintsTheNextPageAtATerminal(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for i := 0; i < 3; i++ {
		if _, err := st.CreateBuild(repo.ID, "unit", "aaa", "main", `["true"]`, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.Term = Term{Cols: 100}
	if code := Dispatch(c, []string{"build", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(c.Stdout.(*bytes.Buffer).String(), "next\t") {
		t.Errorf("cursor row on stdout at a terminal")
	}
	want := "more: gitbay build list " + repo.Path() + " --limit 2 --cursor "
	if !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr = %q, want %q…", errOut.String(), want)
	}
}
```

Add the imports the file needs (`bytes`, `strings`, `protocol`, `store`).

- [ ] **Step 2: Run and see it fail**

Run: `go test ./internal/control -run TestEmitPageHints -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

`Ctx` gains, after `Cmd Command`:

```go
	// Argv is the command's arguments after its path, global flags
	// removed, so output can print a command to run next.
	Argv []string
```

In `Dispatch`, after the `--json` stripping loop builds `args`: `c.Argv = args`.

`emitPage`'s plain closure:

```go
	return c.emit(out{items, next}, func(w io.Writer) {
		plain(w)
		if next == "" {
			return
		}
		if c.Term.Cols == 0 {
			fmt.Fprintf(w, "next\t%s\n", next)
			return
		}
		var again []string
		for i := 0; i < len(c.Argv); i++ {
			if c.Argv[i] == "--cursor" {
				i++
				continue
			}
			again = append(again, c.Argv[i])
		}
		fmt.Fprintf(c.Stderr, "more: gitbay %s %s --cursor %s\n", joinPath(c.Cmd.Path), strings.Join(again, " "), next)
	})
```

- [ ] **Step 4: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/control.go internal/control/cursor.go internal/control/cursor_test.go
git commit -m "control: the next page as a command on stderr at a terminal" -m "Ref #254"
```

### Task 2.5: the CLI sends `GITBAY_TERM`; `--no-color`; tabwriter removed

**Files:**
- Modify: `cmd/gitbay/ssh.go` (`runSSH`, `sshCapture` unchanged; remove `listVerbs`, `alignColumns`, the `tabwriter`)
- Modify: `cmd/gitbay/main.go` (`main` strips `--no-color`)
- Test: `cmd/gitbay/term_test.go` (create)

**Interfaces:**
- Produces:
  - `var noColor bool` (package `main`)
  - `func termValue(isTerminal bool, cols int, env func(string) string) string`
  - `func stripNoColor(args []string) ([]string, bool)`

- [ ] **Step 1: Write the failing test**

```go
package main

import "testing"

func TestTermValue(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		tty     bool
		cols    int
		env     map[string]string
		noColor bool
		want    string
	}{
		{true, 120, nil, false, "120,color"},
		{false, 120, nil, false, ""},
		{true, 30, nil, false, ""},
		{true, 120, map[string]string{"NO_COLOR": "1"}, false, "120"},
		{true, 120, map[string]string{"TERM": "dumb"}, false, "120"},
		{true, 120, nil, true, "120"},
	}
	for _, c := range cases {
		noColor = c.noColor
		if got := termValue(c.tty, c.cols, env(c.env)); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
	noColor = false
}

func TestStripNoColor(t *testing.T) {
	args, ok := stripNoColor([]string{"gitbay", "issue", "list", "--no-color", "--state", "all"})
	if !ok || len(args) != 5 || args[3] != "--state" {
		t.Errorf("got %v %v", args, ok)
	}
}
```

- [ ] **Step 2: Run and see it fail**

Run: `go test ./cmd/gitbay -run 'TestTermValue|TestStripNoColor' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

In `cmd/gitbay/ssh.go`, delete `listVerbs` and `alignColumns`, and add:

```go
// noColor is --no-color, stripped from argv in main.
var noColor bool

// termValue is GITBAY_TERM for this invocation: the terminal's width,
// and whether colour is wanted. Empty when stdout is not a terminal,
// so piped output stays the rows stock ssh prints.
func termValue(isTerminal bool, cols int, env func(string) string) string {
	if !isTerminal || cols < 40 {
		return ""
	}
	v := strconv.Itoa(cols)
	if !noColor && env("NO_COLOR") == "" && env("TERM") != "dumb" {
		v += ",color"
	}
	return v
}

// stripNoColor removes --no-color wherever it appears.
func stripNoColor(args []string) ([]string, bool) {
	out := args[:0:0]
	found := false
	for _, a := range args {
		if a == "--no-color" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}
```

In `runSSH`, replace the tabwriter block. Before building `args`'s destination:

```go
	fd := int(os.Stdout.Fd())
	cols := 0
	isTTY := term.IsTerminal(fd)
	if isTTY {
		cols, _, _ = term.GetSize(fd)
	}
	if v := termValue(isTTY, cols, os.Getenv); v != "" && !slices.Contains(serverArgv, "--json") {
		args = append(args, "-o", "SetEnv=GITBAY_TERM="+v)
	}
```

placed after `args := sshArgs(t.inst)` and before the destination is appended (if Task 2.3 took Step 6, prepend `"--term="+v` to `serverArgv` instead). Remove `cmd.Stdout = tw` handling and the `text/tabwriter` import. In `main()`:

```go
func main() {
	os.Args, noColor = stripNoColor(os.Args)
	if err := newRoot().Execute(); err != nil {
```

- [ ] **Step 4: Run**

Run: `go build ./... && go vet ./cmd/gitbay && go test ./cmd/gitbay -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbay
git commit -m "gitbay: send GITBAY_TERM at a terminal; --no-color; drop client-side column padding" -m "Ref #254"
```

### Task 2.6: every list command on `table` (issues, MRs, builds, releases, labels, milestones, search, explore)

**Files:**
- Modify: `internal/control/issue.go`, `mr.go`, `build.go`, `release.go`, `label.go`, `orglabel.go`, `milestone.go`, `search.go`, `explore.go`
- Test: existing tests in those files' `_test.go`

**Recipe (applies to Tasks 2.6, 2.7, 2.8):**

A list site is an `emit`/`emitPage` whose plain formatter prints one row per item. For each:

1. Keep the column order exactly as the `fmt.Fprintf` prints it today, so plain bytes are unchanged apart from timestamps.
2. Choose a cell per column:
   - identifier (`#n`, `!n`, a path, a tag, a sha, a fingerprint, a name that is the row's key) → `cRef`
   - state, status, visibility, role → `cState`
   - stored timestamp → `cAge`
   - count → `cNum` (or `cText` if the value is already a string such as `3 asset(s)`)
   - the title or description → `cFlex` (one per table)
   - anything else → `cText`
3. Header: one short capitalised word per column (`#`, `STATE`, `TITLE`, `AUTHOR`, `UPDATED`, `TAG`, `JOB`, `STATUS`, `SHA`, `REF`, `NAME`, `DESCRIPTION`, `ASSETS`, `DUE`, `COUNT`, `KIND`, `PATH`, `WHEN`).
4. Replace the loop:

```go
return c.emit(ds, func(w io.Writer) {
	tb := c.table(w, "#", "STATE", "TITLE", "AUTHOR")
	for _, d := range ds {
		tb.row(cRef(fmt.Sprintf("#%d", d.Number)), cState(d.State), cFlex(d.Title), cText(d.Author))
	}
	tb.flush()
})
```

5. A column printed with a literal prefix or suffix (`due 2027-01-01`, `via team`) keeps it inside the cell string.
6. A row printed conditionally (optional trailing column) prints `""` in that cell so every row has the same number of cells.

- [ ] **Step 1: Convert** every list site in the files above, one file at a time. `release list` columns: `TAG` `cRef`, `TITLE` `cFlex`, `ASSETS` `cText`.

- [ ] **Step 2: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS. A failure on a timestamp means the test asserted a stored value; change its expectation to `stamp(value)`'s form (`2026-09-23T23:26:00Z`). A failure anywhere else means a column changed order or content: fix the conversion, not the test.

- [ ] **Step 3: Commit**

```bash
git add internal/control
git commit -m "control: issue, mr, build, release, label, milestone, search and explore lists as tables" -m "Ref #254"
```

### Task 2.7: list commands in repo, read, sig, status, wiki, mirror, pages, runners, webhooks, diff threads

**Files:**
- Modify: `internal/control/repo.go` (the six sites left after `repo list`), `read.go`, `sig.go`, `status.go`, `wiki.go`, `mirrorcmd.go`, `pagescmd.go`, `runnerrepo.go`, `webhook.go`, `diffcomment.go`

- [ ] **Step 1: Convert** with the recipe in Task 2.6. `read.go`'s `log`-like listings: `SHA` `cRef`, `DATE` `cAge`, `AUTHOR` `cText`, `SUBJECT` `cFlex`. Sites that print file content (a blob, a diff, a log's body) are not lists; leave them.

- [ ] **Step 2: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS, with the same rule for failures as Task 2.6.

- [ ] **Step 3: Commit**

```bash
git add internal/control
git commit -m "control: repository, history, status, wiki, mirror, runner and webhook lists as tables" -m "Ref #254"
```

### Task 2.8: list commands in accounts, orgs, admin, notifications, snippets, feed, dashboard

**Files:**
- Modify: `internal/control/admin.go`, `audit.go`, `identity.go`, `deploykey.go`, `token.go`, `web.go`, `register.go`, `notifications.go`, `org.go`, `teams.go`, `snippet.go`, `dashboard.go`
- Not `control.go`'s `help` listing: Part 4 rewrites it.

- [ ] **Step 1: Convert** with the recipe in Task 2.6. `admin.go:287` and `token.go:102` format `time.RFC3339` themselves: pass the stored string to `cAge` (or, for a `used`/`expires` word column, keep the word and format the time with `stamp` when plain, `relAge(…, termNow())` at a terminal).

`dashboard`: each section keeps its title line; the rows under it become a table per section. Plain output keeps today's two-space indent and tabs, so for the dashboard only, write the plain branch as it is and use `c.table` only when `c.Term.Cols > 0`:

```go
section := func(title string, header []string, rows [][]cell) {
	fmt.Fprintln(w, title)
	if len(rows) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	if c.Term.Cols == 0 {
		for _, r := range rows {
			parts := make([]string, len(r))
			for i, cl := range r {
				parts[i] = cl.s
				if cl.kind == kindAge {
					parts[i] = stamp(cl.s)
				}
			}
			fmt.Fprintf(w, "  %s\n", strings.Join(parts, "\t"))
		}
		return
	}
	tb := c.table(w, header...)
	for _, r := range rows {
		tb.row(r...)
	}
	tb.flush()
}
```

In terminal mode the section title is bold: `fmt.Fprintln(w, c.Term.paint(sgrBold, title))` in place of the plain `Fprintln` when `c.Term.Cols > 0`.

- [ ] **Step 2: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/control
git commit -m "control: account, org, admin, notification, snippet, feed and dashboard lists as tables" -m "Ref #254"
```

### Task 2.9: every read command, plain and at 60 columns, over ssh

**Files:**
- Modify: `e2e/readonly_test.go` (the loop at line ~174)

- [ ] **Step 1: Extend the loop**

After the existing `inst.ssh(...)` call and its exit check, add:

```go
		argv := append(append([]string{}, cmd.Path...), args...)
		plainOut, _, _ := inst.sshTerm(t, aliceKey, "", argv...)
		if strings.Contains(plainOut, "\x1b") {
			t.Errorf("%s: SGR bytes in plain output", path)
		}
		termOut, _, _ := inst.sshTerm(t, aliceKey, "60,color", argv...)
		if !rawOutput[path] {
			for _, line := range strings.Split(termOut, "\n") {
				if w := displayCells(stripSGRe2e(line)); w > 60 {
					t.Errorf("%s: line of %d cells at 60 columns: %q", path, w, line)
					break
				}
			}
		}
```

and before the loop:

```go
	// rawOutput prints content verbatim (a file, a log, a diff) and is
	// not fitted to the terminal.
	rawOutput := map[string]bool{}
```

with helpers at the bottom of the file (e2e does not import unexported control code):

```go
func stripSGRe2e(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "")
}

func displayCells(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me):
		case width.LookupRune(r).Kind() == width.EastAsianWide || width.LookupRune(r).Kind() == width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}
```

(imports `unicode`, `golang.org/x/text/width`.)

- [ ] **Step 2: Run**

Run: `go test ./e2e -run TestReadOnlyCommandsWriteNothing -count=1`

For each width failure: if the command's stdout is verbatim content (file, log, diff, raw asset), add its path to `rawOutput` with nothing else; otherwise the list site was missed or a cell kind is wrong, fix it in `internal/control`. Show commands will fail here until Part 3: add every `* show` path that fails to `rawOutput` with the comment `// until the view layout (Part 3)`, and Part 3 removes them.

Expected in the end: PASS.

- [ ] **Step 3: Commit and open MR 2**

```bash
git add e2e/readonly_test.go
git commit -m "e2e: read commands carry no SGR when plain and fit 60 columns at a terminal" -m "Ref #254"
git push -u origin cli-output-tables
gitbay mr create --source cli-output-tables --target main --title "CLI output: GITBAY_TERM and terminal tables"
```

Before merging, check at a real terminal against a local instance or after deploy: `gitbay issue list`, `gitbay build list --limit 5`, `gitbay issue list | cat`.

---

# Part 3: show views and the pager (branch `cli-output-views`)

### Task 3.1: `internal/termtext`, markdown

**Files:**
- Create: `internal/termtext/termtext.go`, `internal/termtext/markdown.go`
- Test: `internal/termtext/markdown_test.go`, `internal/termtext/testdata/*.md`, `*.golden`

**Interfaces:**
- Produces:
  - `type Options struct { Width int; Color bool; Base string }` — `Width` 0 is plain: no wrapping, no SGR. `Base` is the site URL for relative links.
  - `func Render(src, format string, o Options) string` — `format` `"org"` renders org, anything else markdown.
  - `func Markdown(src string, o Options) string`
  - `func Inline(src, format string) string` — one line, links as their text only, no SGR (for event lines).

- [ ] **Step 1: Write the golden test**

`internal/termtext/markdown_test.go`:

```go
package termtext

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestMarkdownGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []struct {
		name string
		opt  Options
	}{
		{"doc.md.plain.golden", Options{Base: "https://forge.test"}},
		{"doc.md.60.golden", Options{Width: 60, Base: "https://forge.test"}},
		{"doc.md.60color.golden", Options{Width: 60, Color: true, Base: "https://forge.test"}},
	} {
		golden(t, o.name, Markdown(string(src), o.opt))
	}
}

func TestMarkdownWidth(t *testing.T) {
	src, _ := os.ReadFile("testdata/doc.md")
	out := Markdown(string(src), Options{Width: 60, Color: true})
	inCode := false
	for _, line := range strings.Split(out, "\n") {
		plain := stripSGR(line)
		if strings.HasPrefix(plain, "    ") {
			inCode = true
		} else if plain != "" {
			inCode = false
		}
		if !inCode && cells(plain) > 60 {
			t.Errorf("line of %d cells: %q", cells(plain), plain)
		}
	}
}

func TestInlineDropsLinkTargets(t *testing.T) {
	got := Inline("referenced in commit [6c4d1e1454](/krz/gitbay/commit/6c4d) by [cmc](/cmc): landing", "md")
	if got != "referenced in commit 6c4d1e1454 by cmc: landing" {
		t.Errorf("Inline = %q", got)
	}
}
```

`internal/termtext/testdata/doc.md` covers every node the renderer handles:

````markdown
# A heading

A paragraph long enough to wrap at sixty columns, with **strong** and *emphasis*, `code`, a [link](https://example.com/page), a [forge link](/krz/gitbay/issues/1), an autolink <https://example.com>, and ~~struck~~ text.

- one
- two, which is long enough that its continuation line has to hang under the text rather than the bullet
  - nested

1. first
2. second

- [x] done
- [ ] open

> quoted text

```go
func main() { fmt.Println("a line longer than sixty columns stays on one line, unwrapped") }
```

![alt text](/img.png)

---

| a | b |
|---|---|
| 1 | 2 |
````

- [ ] **Step 2: Run and see it fail**

Run: `go test ./internal/termtext -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/termtext/termtext.go`:

```go
// Package termtext renders markdown and org to text for a terminal:
// wrapped to a width, links reduced to their text, code highlighted
// with 16 colours. Width 0 is plain: no wrapping and no SGR, for
// piped output.
package termtext

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2/quick"
	"golang.org/x/text/width"
)

type Options struct {
	Width int
	Color bool
	Base  string
}

func Render(src, format string, o Options) string {
	if format == "org" {
		return Org(src, o)
	}
	return Markdown(src, o)
}

const (
	sgrReset     = "\x1b[0m"
	sgrBold      = "\x1b[1m"
	sgrDim       = "\x1b[2m"
	sgrUnderline = "\x1b[4m"
)

// out collects rendered lines. Every block goes through it so the
// prefixes (indent, list marker, quote bar) and the wrap live in one
// place.
type out struct {
	o      Options
	b      strings.Builder
	noURLs bool // links as their text only (Inline)
}

func (w *out) paint(sgr, s string) string {
	if !w.o.Color || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

// para writes s wrapped to the width, the first line after first and
// the rest after rest. Hard breaks in s ("\n") start a new line.
func (w *out) para(s, first, rest string) {
	prefix := first
	for _, hard := range strings.Split(s, "\n") {
		for _, line := range wrap(hard, w.o.Width-cells(rest)) {
			w.b.WriteString(prefix + line + "\n")
			prefix = rest
		}
	}
}

// code writes lines verbatim under prefix plus four spaces,
// highlighted when colour is on.
func (w *out) code(src, lang, prefix string) {
	src = strings.TrimRight(src, "\n")
	if w.o.Color && w.o.Width > 0 {
		var hb bytes.Buffer
		if lang == "" {
			lang = "plaintext"
		}
		if quick.Highlight(&hb, src, lang, "terminal16", "monokai") == nil {
			src = strings.TrimRight(hb.String(), "\n")
		}
	}
	for _, line := range strings.Split(src, "\n") {
		w.b.WriteString(prefix + "    " + line + "\n")
	}
}

func (w *out) rule(prefix string) {
	w.b.WriteString(prefix + w.paint(sgrDim, "───") + "\n")
}

func (w *out) blank() { w.b.WriteString("\n") }

func (w *out) String() string {
	return strings.TrimRight(w.b.String(), "\n") + "\n"
}

// link is a link as terminal text: its text, then the target when the
// target says something the text does not. Relative targets are made
// absolute against Base.
func (w *out) link(text, target string) string {
	if w.noURLs && text != "" {
		return text
	}
	if strings.HasPrefix(target, "/") && w.o.Base != "" {
		target = strings.TrimRight(w.o.Base, "/") + target
	}
	if text == "" {
		return target
	}
	if target == "" || target == text || strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://") == text {
		return text
	}
	return text + " (" + target + ")"
}

// wrap breaks s at spaces into lines of at most width cells. A word
// wider than width is a line of its own. width <= 0 is no wrapping.
func wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	var cur string
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case cells(cur)+1+cells(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me) || r == '‍':
		case width.LookupRune(r).Kind() == width.EastAsianWide || width.LookupRune(r).Kind() == width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```

`internal/termtext/markdown.go`:

```go
package termtext

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// md parses as the web does (CommonMark plus GFM); raw HTML is dropped
// there and here.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func Markdown(src string, o Options) string {
	return renderMarkdown(src, &out{o: o})
}

func renderMarkdown(src string, w *out) string {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	r := mdRenderer{w: w, src: source}
	r.blocks(doc, "", "")
	return w.String()
}

// Inline is src as one line of plain text, links reduced to their
// text, for event lines.
func Inline(src, format string) string {
	w := &out{noURLs: true}
	var s string
	if format == "org" {
		s = renderOrg(src, w)
	} else {
		s = renderMarkdown(src, w)
	}
	return strings.Join(strings.Fields(s), " ")
}

type mdRenderer struct {
	w   *out
	src []byte
}

// blocks renders n's children with a blank line between them. The
// first child's first line is prefixed by first, every other line by
// rest.
func (r mdRenderer) blocks(n ast.Node, first, rest string) {
	p := first
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c != n.FirstChild() {
			r.w.blank()
		}
		r.block(c, p, rest)
		p = rest
	}
}

func (r mdRenderer) block(n ast.Node, first, rest string) {
	switch n := n.(type) {
	case *ast.Heading:
		r.w.para(r.w.paint(sgrBold, r.inline(n)), first, rest)
	case *ast.Paragraph:
		r.w.para(r.inline(n), first, rest)
	case *ast.TextBlock:
		r.w.para(r.inline(n), first, rest)
	case *ast.List:
		i := n.Start
		p := first
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			marker := "• "
			if n.IsOrdered() {
				marker = fmt.Sprintf("%d. ", i)
				i++
			}
			hang := rest + strings.Repeat(" ", cells(marker))
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if c == item.FirstChild() {
					r.block(c, p+marker, hang)
				} else {
					if !n.IsTight {
						r.w.blank()
					}
					r.block(c, hang, hang)
				}
			}
			p = rest
		}
	case *ast.FencedCodeBlock:
		r.w.code(r.lines(n), string(n.Language(r.src)), rest)
	case *ast.CodeBlock:
		r.w.code(r.lines(n), "", rest)
	case *ast.Blockquote:
		bar := r.w.paint(sgrDim, "│ ")
		r.blocks(n, first+bar, rest+bar)
	case *ast.ThematicBreak:
		r.w.rule(first)
	case *ast.HTMLBlock:
		// Dropped, as the web drops it.
	default:
		// GFM tables and anything else: the source, as a code block.
		r.w.code(r.lines(n), "", rest)
	}
}

func (r mdRenderer) lines(n ast.Node) string {
	var b strings.Builder
	ls := n.Lines()
	for i := 0; i < ls.Len(); i++ {
		seg := ls.At(i)
		b.Write(seg.Value(r.src))
	}
	return b.String()
}

func (r mdRenderer) inline(n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(r.src))
			switch {
			case c.HardLineBreak():
				b.WriteString("\n")
			case c.SoftLineBreak():
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			b.WriteString(r.inline(c))
		case *ast.Emphasis:
			sgr := sgrUnderline
			if c.Level == 2 {
				sgr = sgrBold
			}
			b.WriteString(r.w.paint(sgr, r.inline(c)))
		case *ast.Link:
			b.WriteString(r.w.link(r.inline(c), string(c.Destination)))
		case *ast.AutoLink:
			u := string(c.URL(r.src))
			b.WriteString(r.w.link(u, u))
		case *ast.Image:
			b.WriteString("[image: " + r.inline(c) + "]")
		case *ast.RawHTML:
		case *east.TaskCheckBox:
			if c.IsChecked {
				b.WriteString("[x] ")
			} else {
				b.WriteString("[ ] ")
			}
		default:
			b.WriteString(r.inline(c))
		}
	}
	return b.String()
}
```

`Inline` calls `renderOrg`, which Task 3.2 adds. Until then, add a stub to `org.go` so the package compiles: `func renderOrg(src string, w *out) string { return src }`.

- [ ] **Step 4: Generate the golden files and read them**

Run: `go test ./internal/termtext -run TestMarkdownGolden -update -count=1`, then open each `testdata/doc.md.*.golden` and check by eye:
- plain: no `\x1b`, paragraphs unwrapped, `link (https://example.com/page)`, `forge link (https://forge.test/krz/gitbay/issues/1)`, the autolink once, `[image: alt text]`, bullets `•`, `[x] done`, the table as its source indented four spaces.
- 60: no non-code line over 60 cells; the nested item indented under its parent's text.
- 60color: bold heading, underlined emphasis, dim quote bar, SGR in the code line.

Fix the renderer until each reads right, regenerate, then:

Run: `go test ./internal/termtext -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/termtext
git commit -m "termtext: markdown for a terminal" -m "Ref #254"
```

### Task 3.2: `internal/termtext`, org

**Files:**
- Create: `internal/termtext/org.go`
- Test: `internal/termtext/org_test.go`, `testdata/doc.org`, goldens

**Interfaces:**
- Produces: `func Org(src string, o Options) string`, `func renderOrg(src string, w *out) string`.

- [ ] **Step 1: Check go-org's node types**

Run: `go doc github.com/niklasfasching/go-org/org | grep -E '^type|^func String'`
The code below uses `Headline{Lvl, Title, Children}`, `Paragraph{Children}`, `List{Kind, Items}`, `ListItem{Bullet, Children}`, `DescriptiveListItem{Term, Details}`, `Block{Name, Parameters, Children}`, `Example{Children}`, `HorizontalRule`, `Text{Content}`, `LineBreak`, `ExplicitLineBreak`, `Emphasis{Kind, Content}`, `RegularLink{Protocol, Description, URL}`, `Keyword`, and `org.String(nodes ...Node) string`. Adjust field names to what `go doc` prints.

- [ ] **Step 2: Write the golden test**

`internal/termtext/org_test.go`:

```go
package termtext

import (
	"os"
	"strings"
	"testing"
)

func TestOrgGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/doc.org")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "doc.org.plain.golden", Org(string(src), Options{Base: "https://forge.test"}))
	golden(t, "doc.org.60.golden", Org(string(src), Options{Width: 60, Base: "https://forge.test"}))
	golden(t, "doc.org.60color.golden", Org(string(src), Options{Width: 60, Color: true, Base: "https://forge.test"}))
}

// #+INCLUDE reads nothing from the server's disk.
func TestOrgIncludeIsInert(t *testing.T) {
	got := Org("#+INCLUDE: \"/etc/passwd\"\n\ntext\n", Options{})
	if strings.Contains(got, "root:") {
		t.Fatalf("include read a file: %q", got)
	}
}
```

`testdata/doc.org`:

```org
#+TITLE: ignored keyword

* A heading
A paragraph long enough to wrap at sixty columns, with *bold*, /italic/, _underline_, =verbatim=, ~code~, a [[https://example.com/page][link]], and a bare [[https://example.com]].

- one
- two, which is long enough that its continuation line has to hang under the text rather than the bullet
  - nested

1. first
2. second

- term :: its description

#+BEGIN_SRC go
func main() { fmt.Println("a line longer than sixty columns stays on one line, unwrapped") }
#+END_SRC

#+BEGIN_QUOTE
quoted text
#+END_QUOTE

-----

| a | b |
|---+---|
| 1 | 2 |
```

- [ ] **Step 3: Run and see it fail**

Run: `go test ./internal/termtext -run 'TestOrg' -count=1`
Expected: FAIL to compile.

- [ ] **Step 4: Implement `internal/termtext/org.go`** (replacing the `renderOrg` stub from Task 3.1)

```go
package termtext

import (
	"bytes"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/niklasfasching/go-org/org"
)

func Org(src string, o Options) string {
	return renderOrg(src, &out{o: o})
}

// renderOrg parses with the same restrictions as the web: no file is
// ever read (#+INCLUDE, #+SETUPFILE), and parse warnings go nowhere.
func renderOrg(src string, w *out) string {
	c := org.New()
	c.ReadFile = func(string) ([]byte, error) { return nil, errors.New("org: includes are disabled") }
	c.Log = log.New(io.Discard, "", 0)
	doc := c.Parse(bytes.NewReader([]byte(src)), "")
	r := orgRenderer{w: w}
	r.nodes(doc.Nodes, "", "")
	return w.String()
}

type orgRenderer struct{ w *out }

func (r orgRenderer) nodes(ns []org.Node, first, rest string) {
	p := first
	wrote := false
	for _, n := range ns {
		if skipOrg(n) {
			continue
		}
		if wrote {
			r.w.blank()
		}
		r.block(n, p, rest)
		p, wrote = rest, true
	}
}

func skipOrg(n org.Node) bool {
	switch n.(type) {
	case org.Keyword, org.PropertyDrawer, org.Comment:
		return true
	}
	return false
}

func (r orgRenderer) block(n org.Node, first, rest string) {
	switch n := n.(type) {
	case org.Headline:
		r.w.para(r.w.paint(sgrBold, r.inline(n.Title)), first, rest)
		if len(n.Children) > 0 {
			r.w.blank()
			r.nodes(n.Children, rest, rest)
		}
	case org.Paragraph:
		r.w.para(r.inline(n.Children), first, rest)
	case org.List:
		p := first
		for i, item := range n.Items {
			if i > 0 && n.Kind == "descriptive" {
				r.w.blank()
			}
			switch item := item.(type) {
			case org.ListItem:
				marker := "• "
				if n.Kind == "ordered" {
					marker = item.Bullet + " "
				}
				hang := rest + strings.Repeat(" ", cells(marker))
				r.nodes(item.Children, p+marker, hang)
			case org.DescriptiveListItem:
				term := r.w.paint(sgrBold, r.inline(item.Term))
				r.w.para(term, p, rest)
				r.nodes(item.Details, rest+"  ", rest+"  ")
			default:
				r.w.code(org.String(item), "", rest)
			}
			p = rest
		}
	case org.Block:
		switch strings.ToUpper(n.Name) {
		case "SRC":
			lang := ""
			if len(n.Parameters) > 0 {
				lang = n.Parameters[0]
			}
			r.w.code(org.String(n.Children...), lang, rest)
		case "QUOTE":
			bar := r.w.paint(sgrDim, "│ ")
			r.nodes(n.Children, first+bar, rest+bar)
		default:
			r.w.code(org.String(n.Children...), "", rest)
		}
	case org.Example:
		r.w.code(org.String(n.Children...), "", rest)
	case org.HorizontalRule:
		r.w.rule(first)
	default:
		r.w.code(org.String(n), "", rest)
	}
}

func (r orgRenderer) inline(ns []org.Node) string {
	var b strings.Builder
	for _, n := range ns {
		switch n := n.(type) {
		case org.Text:
			b.WriteString(n.Content)
		case org.LineBreak:
			b.WriteString(" ")
		case org.ExplicitLineBreak:
			b.WriteString("\n")
		case org.Emphasis:
			s := r.inline(n.Content)
			switch n.Kind {
			case "*":
				s = r.w.paint(sgrBold, s)
			case "/", "_":
				s = r.w.paint(sgrUnderline, s)
			}
			b.WriteString(s)
		case org.RegularLink:
			b.WriteString(r.w.link(r.inline(n.Description), n.URL))
		default:
			b.WriteString(org.String(n))
		}
	}
	return b.String()
}
```

Blank lines between list items: go-org separates items with no blank line in a tight list; if the goldens show blank lines between `• one` and `• two`, remove the `blank()` in `nodes` for list children by rendering item children with a local loop that skips it.

- [ ] **Step 5: Generate the goldens and read them**

Run: `go test ./internal/termtext -run TestOrgGolden -update -count=1`. Check by eye as in Task 3.1; `#+TITLE` must not appear. Then:

Run: `go test ./internal/termtext -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/termtext
git commit -m "termtext: org for a terminal" -m "Ref #254"
```

### Task 3.3: `view`, and `issue show` on it

**Files:**
- Create: `internal/control/view.go`
- Modify: `internal/control/issue.go` (`runIssueShow` plain formatter, lines ~231-245)
- Test: `internal/control/view_test.go`

**Interfaces:**
- Consumes: `Term`, `stamp`, `parseStamp`, `cells`, `pad`, `termtext.Render`, `termtext.Inline`.
- Produces:
  - `func (c *Ctx) when(s string) string` — `2006-01-02 15:04 UTC` at a terminal, `stamp(s)` plain.
  - `func (c *Ctx) view(w io.Writer) *view`
  - `func (v *view) title(ref, title, state string)`
  - `func (v *view) fields(kv ...string)` — alternating key, value; empty values skipped.
  - `func (v *view) body(src, format string)`
  - `func (v *view) event(text, format, ts string)`
  - `func (v *view) comment(author, ts, body, format string)`
  - `func (c *Ctx) siteURL(parts ...string) string` — `server.site_url` joined with `/`.

- [ ] **Step 1: Write the failing test**

```go
package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func showIssue(t *testing.T, term Term) string {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	n, err := st.CreateIssue(repo.ID, uid, "A title", "Body with a [link](/x/y).", "md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddIssueComment(issueIDByNumber(t, st, repo.ID, n), 0, "referenced in commit [abc1234567](/o/r/commit/abc) by [alice](/alice)", "md"); err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.Cfg.Server.SiteURL = "https://forge.test"
	c.Term = term
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	return c.Stdout.(*bytes.Buffer).String()
}

func TestIssueShowPlain(t *testing.T) {
	out := showIssue(t, Term{})
	for _, want := range []string{
		"#1  A title  open\n",
		"  author  alice, ",
		"  url     https://forge.test/",
		"Body with a link (https://forge.test/x/y).",
		"  · referenced in commit abc1234567 by alice  ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "](") {
		t.Errorf("markup or SGR in plain view:\n%s", out)
	}
}

func TestIssueShowTerminal(t *testing.T) {
	out := showIssue(t, Term{Cols: 60, Color: true})
	if !strings.Contains(out, sgrGreen+"open"+sgrReset) {
		t.Errorf("state not coloured:\n%s", out)
	}
	if !strings.Contains(out, " UTC") {
		t.Errorf("no web-format timestamp:\n%s", out)
	}
	for _, line := range strings.Split(stripSGR(out), "\n") {
		if cells(line) > 60 {
			t.Errorf("line over 60 cells: %q", line)
		}
	}
}
```

Before writing this, read `internal/store/issues.go` for the real names and signatures of the create-issue and add-comment functions and for how a system comment is stored (author `system`, or an author id of 0, or a kind column), and adjust `showIssue` to match. Write `issueIDByNumber` only if no lookup exists (`st.IssueByNumber(repoID, n)` likely does).

- [ ] **Step 2: Run and see it fail**

Run: `go test ./internal/control -run TestIssueShow -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/control/view.go`**

```go
package control

import (
	"io"
	"strings"

	"gitbay.org/gitbay/internal/termtext"
)

// when is a stored timestamp in a view: the web's format at a
// terminal, RFC3339 to the second when plain.
func (c *Ctx) when(s string) string {
	if c.Term.Cols == 0 {
		return stamp(s)
	}
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	return t.Format("2006-01-02 15:04 UTC")
}

// siteURL is the instance's address with path segments appended.
func (c *Ctx) siteURL(parts ...string) string {
	return strings.TrimRight(c.Cfg.Server.SiteURL, "/") + "/" + strings.Join(parts, "/")
}

// view lays out a show: a title line, aligned fields, a body, events,
// comments. Plain output is the same lines without colour or wrapping.
type view struct {
	c *Ctx
	w io.Writer
}

func (c *Ctx) view(w io.Writer) *view { return &view{c: c, w: w} }

func (v *view) opts() termtext.Options {
	return termtext.Options{Width: max(0, v.c.Term.Cols-2), Color: v.c.Term.Color, Base: v.c.Cfg.Server.SiteURL}
}

func (v *view) title(ref, title, state string) {
	t := v.c.Term
	io.WriteString(v.w, ref+"  "+t.paint(sgrBold, title)+"  "+t.paint(stateColor(state), state)+"\n")
}

// fields prints key/value pairs aligned on the widest key, skipping
// empty values.
func (v *view) fields(kv ...string) {
	wide := 0
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			wide = max(wide, cells(kv[i]))
		}
	}
	io.WriteString(v.w, "\n")
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		io.WriteString(v.w, "  "+v.c.Term.paint(sgrDim, pad(kv[i], wide))+"  "+kv[i+1]+"\n")
	}
}

func (v *view) body(src, format string) {
	if strings.TrimSpace(src) == "" {
		return
	}
	io.WriteString(v.w, "\n")
	for _, line := range strings.Split(strings.TrimRight(termtext.Render(src, format, v.opts()), "\n"), "\n") {
		if line == "" {
			io.WriteString(v.w, "\n")
			continue
		}
		io.WriteString(v.w, "  "+line+"\n")
	}
}

// event is one line for a system comment: its text without link
// targets, the time at the right edge at a terminal.
func (v *view) event(text, format, ts string) {
	line := "· " + termtext.Inline(text, format)
	when := v.c.when(ts)
	if cols := v.c.Term.Cols; cols > 0 {
		room := cols - 2 - 2 - cells(when)
		line = pad(clip(line, room), room)
	}
	io.WriteString(v.w, "  "+v.c.Term.paint(sgrDim, line+"  "+when)+"\n")
}

func (v *view) comment(author, ts, body, format string) {
	head := "── " + author + ", " + v.c.when(ts) + " "
	if cols := v.c.Term.Cols; cols > 0 {
		head += strings.Repeat("─", max(0, cols-cells(head)))
	}
	io.WriteString(v.w, "\n"+v.c.Term.paint(sgrDim, head)+"\n")
	v.body(body, format)
}
```

`issue show`'s formatter:

```go
	return c.emit(d, func(w io.Writer) {
		v := c.view(w)
		v.title(fmt.Sprintf("#%d", d.Number), d.Title, d.State)
		v.fields(
			"author", d.Author+", "+c.when(d.CreatedAt),
			"assignees", strings.Join(d.Assignees, ", "),
			"labels", strings.Join(d.Labels, ", "),
			"milestone", d.Milestone,
			"url", c.siteURL(repo.Path(), "issues", strconv.FormatInt(d.Number, 10)),
		)
		v.body(d.Body, d.BodyFormat)
		events := false
		for _, cm := range cs {
			if cm.Author != "system" {
				continue
			}
			if !events {
				io.WriteString(w, "\n")
				events = true
			}
			v.event(cm.Body, cm.BodyFormat, cm.CreatedAt)
		}
		for _, cm := range cs {
			if cm.Author == "system" {
				continue
			}
			v.comment(cm.Author, cm.CreatedAt, cm.Body, cm.BodyFormat)
		}
	})
```

Remove the `_ = repo` line. Use the system-comment test that Step 1's store reading found in place of `cm.Author == "system"` if it differs. Check the web issue URL segment (`issues`) against `internal/httpd/routes.go`.

In plain mode `event` prints `  · <text>  <stamp>` with two spaces between, which the test expects.

- [ ] **Step 4: Run**

Run: `go build ./... && go test ./internal/control -count=1`
Expected: PASS. Existing tests asserting the old `issue show` text (`#1 A title [open] by alice`, `--- alice at`) change to the new layout; e2e tests grepping `issue show` output: `grep -rn '"issue", "show"' e2e/` and update their assertions to substrings the new layout prints.

- [ ] **Step 5: Commit**

```bash
git add internal/control e2e
git commit -m "control: view layout; issue show on it" -m "Ref #254"
```

### Task 3.4: every other show on `view`

**Files:**
- Modify: `internal/control/mr.go` (`mr show`), `build.go` (`build show`), `release.go` (`release show`), `snippet.go` (`snippet show`), `repo.go` (`repo show`, `repo settings show`), `org.go` (`org show`), `teams.go` (`org team show`), `profile.go` (`profile show`), `admin.go` (`admin user show`), `wiki.go` (`wiki show`), `notifications.go` (`notifications settings show`), `theme.go` (`web theme show`)

- [ ] **Step 1: Convert each** with this mapping, reading each formatter first:
  - The first line becomes `v.title(<identifier>, <name or title>, <state or visibility>)`. A show with no state passes `""` (the trailing space is harmless; `title` may skip it when empty — make it do so).
  - Every `key: value` line becomes a pair in one `v.fields(...)` call; timestamps through `c.when`; add `"url", c.siteURL(...)` where the web has a page for the object.
  - A body (MR description, release notes, snippet file text is content, not markup: print it verbatim after a blank line; wiki page source → `v.body(src, format)` with format from the file extension, `org` for `.org`).
  - Comments and events on `mr show` exactly as on `issue show`; reviews and checks become `fields` rows (`check  ci/build success, 22s`) or a `table` after the fields when there is more than one.
  - Single-value shows (`web theme show`, `notifications settings show`) keep their one-line output if they print one line today.

- [ ] **Step 2: Remove the Part 2 `rawOutput` entries** added "until the view layout" in `e2e/readonly_test.go`.

- [ ] **Step 3: Run**

Run: `go test ./internal/control -count=1 && go test ./e2e -run TestReadOnlyCommandsWriteNothing -count=1`
Expected: PASS. A show that exceeds 60 columns outside a code block is a layout bug; fix it in the formatter.

- [ ] **Step 4: Commit**

```bash
git add internal/control e2e
git commit -m "control: every show on the view layout" -m "Ref #254"
```

### Task 3.5: the pager

**Files:**
- Modify: `cmd/gitbay/ssh.go` (`runSSH` → `runSSHPaged`)
- Modify: `cmd/gitbay/main.go` (`runPass`)
- Test: `cmd/gitbay/term_test.go`

**Interfaces:**
- Produces:
  - `func pagerArgv(env func(string) (string, bool)) []string` — nil means no pager.
  - `func pages(server, args []string) bool`
  - `func runSSHPaged(t target, serverArgv []string, stdin io.Reader, page bool) int`; `runSSH(t, argv, stdin)` becomes `runSSHPaged(t, argv, stdin, false)`.

- [ ] **Step 1: Write the failing test**

```go
func TestPagerArgv(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, "less"},
		{map[string]string{"PAGER": "more -s"}, "more -s"},
		{map[string]string{"PAGER": "more", "GITBAY_PAGER": "bat -p"}, "bat -p"},
		{map[string]string{"PAGER": "more", "GITBAY_PAGER": ""}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(pagerArgv(env(c.env)), " "); got != c.want {
			t.Errorf("%v: got %q want %q", c.env, got, c.want)
		}
	}
}

func TestPages(t *testing.T) {
	yes := [][]string{{"issue", "show"}, {"mr", "diff"}, {"build", "log"}, {"repo", "log"}}
	for _, s := range yes {
		if !pages(s, nil) {
			t.Errorf("%v should page", s)
		}
	}
	if pages([]string{"build", "log"}, []string{"--follow"}) {
		t.Error("build log --follow must not page")
	}
	if pages([]string{"issue", "show"}, []string{"--json"}) {
		t.Error("--json must not page")
	}
	if pages([]string{"issue", "list"}, nil) {
		t.Error("list must not page")
	}
}
```

(add `strings` to the test imports.)

- [ ] **Step 2: Run and see it fail**

Run: `go test ./cmd/gitbay -run 'TestPagerArgv|TestPages' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

```go
// pagerArgv is the pager to run long output through: GITBAY_PAGER,
// then PAGER, then less. An empty GITBAY_PAGER turns paging off.
func pagerArgv(env func(string) (string, bool)) []string {
	if v, ok := env("GITBAY_PAGER"); ok {
		return strings.Fields(v)
	}
	if v, ok := env("PAGER"); ok && v != "" {
		return strings.Fields(v)
	}
	return []string{"less"}
}

// pages reports whether a command's output goes through the pager at a
// terminal: views, diffs and logs, never a follow or JSON.
func pages(server, args []string) bool {
	if len(server) == 0 || slices.Contains(args, "--json") || slices.Contains(args, "--follow") {
		return false
	}
	switch server[len(server)-1] {
	case "show", "diff", "log":
		return true
	}
	return false
}
```

In `runSSHPaged`, after `cmd` is built and before `cmd.Run()`:

```go
	var pager *exec.Cmd
	var pw io.WriteCloser
	if page && term.IsTerminal(int(os.Stdout.Fd())) {
		if argv := pagerArgv(os.LookupEnv); len(argv) > 0 {
			pager = exec.Command(toolpath.Look(argv[0]), argv[1:]...)
			pager.Stdout, pager.Stderr = os.Stdout, os.Stderr
			if _, ok := os.LookupEnv("LESS"); !ok {
				pager.Env = append(os.Environ(), "LESS=FRX")
			}
			if w, err := pager.StdinPipe(); err == nil && pager.Start() == nil {
				pw = w
				cmd.Stdout = pw
			} else {
				pager = nil
			}
		}
	}
	err := cmd.Run()
	if pager != nil {
		pw.Close()
		pager.Wait()
	}
```

The `GITBAY_TERM` value is computed from `os.Stdout` before this block, so the server still sees the terminal. In `runPass`, the final call becomes `runSSHPaged(t, append(o.server, args...), stdin, pages(o.server, args))`.

- [ ] **Step 4: Run**

Run: `go build ./... && go vet ./cmd/gitbay && go test ./cmd/gitbay -count=1`
Expected: PASS. At a terminal against a local instance: `gitbay issue show <n>` opens `less` only when longer than the screen; `GITBAY_PAGER= gitbay issue show <n>` never does; `gitbay issue show <n> | cat` never does.

- [ ] **Step 5: Commit and open MR 3**

```bash
git add cmd/gitbay
git commit -m "gitbay: page views, diffs and logs at a terminal" -m "Ref #254"
git push -u origin cli-output-views
gitbay mr create --source cli-output-views --target main --title "CLI output: show views and the pager"
```

---

# Part 4: help (branch `cli-output-help`)

### Task 4.1: `Flags` and `Examples` on `Command`, and the registry test

**Files:**
- Modify: `internal/control/control.go` (`Flag`, `Command` fields)
- Create: `internal/control/help_test.go`

**Interfaces:**
- Produces:

```go
// Flag is one flag in a command's help.
type Flag struct {
	Name    string // "--state"
	Arg     string // "open|closed|all"; empty for a switch
	Desc    string // what it does, lower case, no full stop
	Default string // empty for none
}
```

`Command` gains `Flags []Flag` and `Examples []string` (each the full argv after the program, repository named).

- [ ] **Step 1: Write the test**

```go
package control

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

var usageFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// Help is written once, in the registry. Every flag in a usage line has
// a description, every description names a flag in the usage line, and
// every command has an example that runs it.
func TestHelpIsComplete(t *testing.T) {
	for _, cmd := range Commands() {
		path := strings.Join(cmd.Path, " ")
		inUsage := map[string]bool{}
		for _, f := range usageFlag.FindAllString(cmd.Usage, -1) {
			if f != "--json" {
				inUsage[f] = true
			}
		}
		described := map[string]bool{}
		for _, f := range cmd.Flags {
			described[f.Name] = true
			if f.Desc == "" {
				t.Errorf("%s: %s has no description", path, f.Name)
			}
			if !inUsage[f.Name] {
				t.Errorf("%s: %s is described but not in the usage", path, f.Name)
			}
		}
		for f := range inUsage {
			if !described[f] {
				t.Errorf("%s: %s is in the usage with no description", path, f)
			}
		}
		if len(cmd.Examples) == 0 {
			t.Errorf("%s: no example", path)
		}
		for _, ex := range cmd.Examples {
			argv, err := protocol.Tokenize(ex)
			if err != nil {
				t.Errorf("%s: example %q: %v", path, ex, err)
				continue
			}
			got, _, ok := Lookup(argv)
			if !ok || !slices.Equal(got.Path, cmd.Path) {
				t.Errorf("%s: example %q runs %v", path, ex, got.Path)
			}
		}
	}
}
```

- [ ] **Step 2: Add the type and fields** to `control.go`; run `go test ./internal/control -run TestHelpIsComplete -count=1`. Expected: FAIL listing every command. Keep the list: Tasks 4.2 and 4.3 fill it.

- [ ] **Step 3: Commit** (the test fails; commit it with the next task instead if CI must stay green per commit — it must, since `ff` merges every commit. Do not commit yet; carry it into Task 4.2's commit.)

### Task 4.2: flag descriptions and examples, work nouns

**Files:**
- Modify: registrations in `internal/control/issue.go`, `mr.go`, `build.go`, `release.go`, `milestone.go`, `label.go`, `orglabel.go`, `search.go`, `explore.go`, `dashboard.go`, `thread.go`, `diffcomment.go`, `status.go`, `wiki.go`, `snippet.go`, `read.go`, `repo.go`, `repo*.go`

**Rules for the text:**
- `Desc`: lower case, no full stop, says what the flag changes, under 50 characters: `which issues`, `only issues carrying this label`, `rows per page`, `continue from the previous page`, `read the body from stdin`.
- `Arg`: the placeholder exactly as the usage line spells it (`<l>`, `open|closed|all`, `-`).
- `Default`: only when the command applies one (`open`, `30`); read the `Run` function to find it.
- Examples: the most common real use first, a second only when a flag combination is worth showing. Repository `krz/gitbay`, user `cmc`, numbers that look real. Stdin examples end in `--file - < notes.md`.

Example, for `issue list`:

```go
register(Command{Path: []string{"issue", "list"},
	Summary: "list issues",
	Usage:   "issue list <owner/name> [--state open|closed|all] [--label <l>] [--assignee <user>] [--author <user>] [--milestone <title>|none] [--search <text>] [--limit <n>] [--cursor <c>]",
	Flags: []Flag{
		{"--state", "open|closed|all", "which issues", "open"},
		{"--label", "<l>", "only issues carrying this label", ""},
		{"--assignee", "<user>", "only issues assigned to this user", ""},
		{"--author", "<user>", "only issues opened by this user", ""},
		{"--milestone", "<title>|none", "only issues in this milestone, or in none", ""},
		{"--search", "<text>", "match title and body", ""},
		{"--limit", "<n>", "rows per page", ""},
		{"--cursor", "<c>", "continue from the previous page", ""},
	},
	Examples: []string{
		"issue list krz/gitbay --label bug --state all",
		"issue list krz/gitbay --assignee cmc",
	},
	ReadOnly: true, Run: runIssueList})
```

(keep the registration's existing field layout and values; only `Flags` and `Examples` are new.) `--limit`/`--cursor` descriptions are the same on every paged command.

- [ ] **Step 1: Fill** every command in the files above.
- [ ] **Step 2: Run** `go test ./internal/control -run TestHelpIsComplete -count=1`. Expected: failures only for commands in files Task 4.3 covers.
- [ ] **Step 3: No commit yet** (the test still fails).

### Task 4.3: flag descriptions and examples, everything else

**Files:**
- Modify: every remaining registration in `internal/control/*.go` (account, keys, tokens, orgs, teams, admin, runners, webhooks, mirrors, notifications, web, profile, register, import, audit, help).

- [ ] **Step 1: Fill** with the rules in Task 4.2. Admin commands' examples use a made-up user (`alice`). Secrets: examples read them from stdin, never argv (`repo secret set krz/gitbay DEPLOY_KEY --file - < key`).
- [ ] **Step 2: Run** `go test ./internal/control -count=1`. Expected: PASS.
- [ ] **Step 3: Commit** (Tasks 4.1–4.3 together)

```bash
git add internal/control
git commit -m "control: every command's flags described, with examples" -m "Ref #254"
```

### Task 4.4: help layouts

**Files:**
- Create: `internal/control/help.go` (move `runHelp`, `helpEntry` and the `help` registration out of `control.go`)
- Test: `internal/control/help_test.go`

**Interfaces:**
- Consumes: `Flag`, `Command.Flags`, `Command.Examples`, `Term`, `hostOf`.
- Produces:
  - `var nounSummaries = map[string]string{...}` — one line per first path element.
  - `func NounSummaries() map[string]string`
  - `helpEntry` gains `Flags []Flag \`json:"flags,omitempty"\`` and `Examples []string \`json:"examples,omitempty"\``; `Flag` gets JSON tags `name`, `arg`, `desc`, `default` (omitempty on the last three).

- [ ] **Step 1: Write the failing tests**

```go
func helpOut(t *testing.T, term Term, prefix ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{Stdout: &out, Stderr: &errOut, Term: term}
	c.Cfg.Server.SiteURL = "https://forge.test"
	if code := Dispatch(c, append([]string{"help"}, prefix...)); code != protocol.ExitOK {
		t.Fatalf("help %v: exit %d: %s", prefix, code, errOut.String())
	}
	return out.String()
}

func TestHelpVerb(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "issue", "list")
	for _, want := range []string{
		"list issues\n",
		"USAGE\n  gitbay issue list [<owner/name>] [flags]\n",
		"FLAGS\n",
		"  --state open|closed|all",
		"which issues (default open)\n",
		"  --json",
		"EXAMPLES\n  gitbay issue list krz/gitbay --label bug --state all\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	plain := helpOut(t, Term{}, "issue", "list")
	if !strings.Contains(plain, "  ssh git@forge.test issue list krz/gitbay --label bug --state all\n") {
		t.Errorf("plain examples not ssh:\n%s", plain)
	}
	if !strings.Contains(plain, "USAGE\n  ssh git@forge.test issue list <owner/name> [flags]\n") {
		t.Errorf("plain usage:\n%s", plain)
	}
}

func TestHelpNoun(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "issue")
	for _, want := range []string{"issues\n", "READ\n", "WRITE\n", "  list ", "  create ", "gitbay issue <verb> --help for flags.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "  list ") > strings.Index(out, "WRITE") {
		t.Errorf("list is not under READ:\n%s", out)
	}
}

func TestEveryNounHasASummary(t *testing.T) {
	for _, cmd := range Commands() {
		if nounSummaries[cmd.Path[0]] == "" {
			t.Errorf("no noun summary for %q", cmd.Path[0])
		}
	}
}
```

- [ ] **Step 2: Run and see them fail**

Run: `go test ./internal/control -run 'TestHelpVerb|TestHelpNoun|TestEveryNounHasASummary' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement `help.go`**

`nounSummaries`: one entry per distinct `cmd.Path[0]` in the registry (list them with `go test -run TestEveryNounHasASummary` output). Reuse the CLI's `group(...)` short texts from `cmd/gitbay/main.go` where the noun matches, so the two agree.

`runHelp`:

```go
func runHelp(c *Ctx, args []string) int {
	prefix := joinPath(args)
	var matched []Command
	for _, cmd := range registry {
		p := joinPath(cmd.Path)
		if prefix == "" || p == prefix || strings.HasPrefix(p, prefix+" ") {
			matched = append(matched, cmd)
		}
	}
	if len(matched) == 0 {
		return c.fail(protocol.ExitNotFound, "no command matches %q; try: help", prefix)
	}
	slices.SortFunc(matched, func(a, b Command) int { return strings.Compare(joinPath(a.Path), joinPath(b.Path)) })
	entries := make([]helpEntry, len(matched))
	for i, cmd := range matched {
		entries[i] = helpEntry{Path: joinPath(cmd.Path), Summary: cmd.Summary, Usage: cmd.Usage, Flags: cmd.Flags, Examples: cmd.Examples}
	}
	return c.emit(entries, func(w io.Writer) {
		switch {
		case prefix == "":
			for _, e := range entries {
				fmt.Fprintf(w, "%-24s %s\n", e.Path, e.Summary)
			}
		case joinPath(matched[0].Path) == prefix:
			c.helpVerb(w, matched[0], matched[1:])
		default:
			c.helpNoun(w, prefix, matched)
		}
	})
}
```

(`matched[0]` is the exact match when one exists, because it sorts first.)

```go
// program is how help spells the command it documents: the CLI at a
// terminal (only the CLI sends GITBAY_TERM), ssh otherwise.
func (c *Ctx) program() string {
	if c.Term.Cols > 0 {
		return "gitbay"
	}
	return "ssh git@" + hostOf(c.Cfg.Server.SiteURL)
}

func (c *Ctx) heading(w io.Writer, s string) {
	fmt.Fprintln(w, c.Term.paint(sgrBold, s))
}

func (c *Ctx) helpVerb(w io.Writer, cmd Command, below []Command) {
	fmt.Fprintln(w, cmd.Summary)
	fmt.Fprintln(w)
	c.heading(w, "USAGE")
	shape := cmd.Usage
	if i := strings.Index(shape, " [--"); i >= 0 {
		shape = shape[:i]
	} else if i := strings.Index(shape, " --"); i >= 0 {
		shape = shape[:i]
	}
	if c.Term.Cols > 0 {
		shape = strings.Replace(shape, "<owner/name>", "[<owner/name>]", 1)
	}
	if len(cmd.Flags) > 0 {
		shape += " [flags]"
	}
	fmt.Fprintf(w, "  %s %s\n", c.program(), shape)
	fmt.Fprintln(w)
	c.heading(w, "FLAGS")
	rows := make([][2]string, 0, len(cmd.Flags)+1)
	for _, f := range cmd.Flags {
		name := f.Name
		if f.Arg != "" {
			name += " " + f.Arg
		}
		desc := f.Desc
		if f.Default != "" {
			desc += " (default " + f.Default + ")"
		}
		rows = append(rows, [2]string{name, desc})
	}
	rows = append(rows, [2]string{"--json", "machine-readable output"})
	wide := 0
	for _, r := range rows {
		wide = max(wide, cells(r[0]))
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %s  %s\n", pad(r[0], wide), r[1])
	}
	if len(cmd.Examples) > 0 {
		fmt.Fprintln(w)
		c.heading(w, "EXAMPLES")
		for _, ex := range cmd.Examples {
			fmt.Fprintf(w, "  %s %s\n", c.program(), ex)
		}
	}
	if len(below) > 0 {
		fmt.Fprintln(w)
		c.heading(w, "SEE ALSO")
		for _, b := range below {
			fmt.Fprintf(w, "  %s %s\n", c.program(), joinPath(b.Path))
		}
	}
}

func (c *Ctx) helpNoun(w io.Writer, prefix string, cmds []Command) {
	head := nounSummaries[strings.Fields(prefix)[0]]
	fmt.Fprintln(w, head)
	fmt.Fprintln(w)
	c.heading(w, "USAGE")
	fmt.Fprintf(w, "  %s %s <verb> ...\n", c.program(), prefix)
	wide := 0
	for _, cmd := range cmds {
		wide = max(wide, cells(strings.TrimPrefix(joinPath(cmd.Path), prefix+" ")))
	}
	for _, section := range []struct {
		title string
		read  bool
	}{{"READ", true}, {"WRITE", false}} {
		first := true
		for _, cmd := range cmds {
			if cmd.ReadOnly != section.read {
				continue
			}
			if first {
				fmt.Fprintln(w)
				c.heading(w, section.title)
				first = false
			}
			verb := strings.TrimPrefix(joinPath(cmd.Path), prefix+" ")
			fmt.Fprintf(w, "  %s  %s\n", pad(verb, wide), cmd.Summary)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s <verb> --help for flags.\n", c.program(), prefix)
}
```

`TestHelpVerb` expects the plain USAGE with `ssh git@forge.test`; confirm `hostOf("https://forge.test")` returns `forge.test`.

- [ ] **Step 4: Run**

Run: `go test ./internal/control -count=1`
Expected: PASS. Any existing test of `help` plain output (grep `"help"` in `internal/control/*_test.go` and `e2e/`) moves to the new layout; `help --json` consumers are unaffected except for the two added fields.

- [ ] **Step 5: Commit**

```bash
git add internal/control
git commit -m "help: noun and verb layouts, flags and examples from the registry" -m "Ref #254"
```

### Task 4.5: the CLI's summaries come from the registry; grouped root help

**Files:**
- Create: `cmd/gitbay/summaries_gen.go` (generated), `cmd/gitbay/summaries_test.go`
- Modify: `cmd/gitbay/main.go` (`pass` loses its `short` parameter; `group` checked against `NounSummaries`; root help function)

**Interfaces:**
- Consumes: `control.Commands()`, `control.NounSummaries()`.
- Produces: `var summaries map[string]string` (package `main`, generated), `var rootSections []rootSection`.

- [ ] **Step 1: Write the test**

`cmd/gitbay/summaries_test.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
)

var updateSummaries = flag.Bool("update", false, "rewrite summaries_gen.go")

// summaries_gen.go is the registry's one-line summaries, so the CLI's
// command list and completions say what help says.
func TestSummariesAreCurrent(t *testing.T) {
	var b strings.Builder
	b.WriteString("// Code generated by TestSummariesAreCurrent -update; DO NOT EDIT.\n\npackage main\n\nvar summaries = map[string]string{\n")
	var lines []string
	for _, cmd := range control.Commands() {
		lines = append(lines, fmt.Sprintf("\t%q: %q,\n", strings.Join(cmd.Path, " "), cmd.Summary))
	}
	slices.Sort(lines)
	b.WriteString(strings.Join(lines, ""))
	b.WriteString("}\n")
	if *updateSummaries {
		os.WriteFile("summaries_gen.go", []byte(b.String()), 0o644)
	}
	got, _ := os.ReadFile("summaries_gen.go")
	if string(got) != b.String() {
		t.Fatal("summaries_gen.go is stale: go test ./cmd/gitbay -run TestSummariesAreCurrent -update")
	}
}

func TestRootSectionsCoverEveryCommand(t *testing.T) {
	seen := map[string]int{}
	for _, s := range rootSections {
		for _, n := range s.names {
			seen[n]++
		}
	}
	for _, c := range newRoot().Commands() {
		name := c.Name()
		if name == "help" || name == "completion" {
			continue
		}
		if seen[name] != 1 {
			t.Errorf("%s is in %d root sections", name, seen[name])
		}
	}
}

func TestGroupsSayWhatTheServerSays(t *testing.T) {
	nouns := control.NounSummaries()
	for _, c := range newRoot().Commands() {
		if s, ok := nouns[c.Name()]; ok && c.Short != s {
			t.Errorf("%s: CLI says %q, server says %q", c.Name(), c.Short, s)
		}
	}
}
```

- [ ] **Step 2: Generate and wire**

Run: `go test ./cmd/gitbay -run TestSummariesAreCurrent -update -count=1` (creates the file; the test compiles only after Step 3, so first create an empty `summaries_gen.go` with `package main\n\nvar summaries = map[string]string{}\n`).

Change `pass`:

```go
func pass(use string, o passOpts) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: summaries[strings.Join(o.server, " ")],
```

and drop the second argument from every `pass(` call in `main.go`:

```bash
sed -i '' -E 's/pass\(("[^"]*"), "([^"\\]|\\.)*", /pass(\1, /' cmd/gitbay/main.go
```

then `go build ./cmd/gitbay` and fix any call the pattern missed by hand. Make each `group(...)` short text equal its `nounSummaries` entry where the noun exists on the server.

Root help:

```go
type rootSection struct {
	title string
	names []string
}

var rootSections = []rootSection{
	{"WORK", []string{"issue", "mr", "build", "release", "milestone", "label", "search"}},
	{"REPOSITORIES", []string{"repo", "wiki", "status", "webhook", "init"}},
	{"YOU", []string{"dashboard", "feed", "notifications", "auth", "profile", "snippet", "web"}},
	{"INSTANCE", []string{"org", "explore", "register", "migrate", "remote", "admin", "audit"}},
}

// rootHelp is gitbay --help: the nouns grouped by what they are for.
func rootHelp(root *cobra.Command) {
	byName := map[string]*cobra.Command{}
	wide := 0
	for _, c := range root.Commands() {
		byName[c.Name()] = c
		wide = max(wide, len(c.Name()))
	}
	fmt.Println("gitbay: command-line client for a gitbay forge")
	fmt.Println()
	fmt.Println("USAGE")
	fmt.Println("  gitbay <command> [<owner/name>] [flags]")
	for _, s := range rootSections {
		fmt.Println()
		fmt.Println(s.title)
		for _, n := range s.names {
			if c := byName[n]; c != nil {
				fmt.Printf("  %-*s  %s\n", wide, n, c.Short)
			}
		}
	}
	fmt.Println()
	fmt.Println("gitbay <command> --help for its verbs; gitbay help <prefix> for the server reference.")
}
```

In `newRoot`, after the commands are added: `root.SetHelpFunc(func(cmd *cobra.Command, args []string) { if cmd == root { rootHelp(root); return }; defaultHelp(cmd, args) })` with `defaultHelp := root.HelpFunc()` captured before the call. `helpCmd`'s bare case calls `rootHelp(root)` instead of `root.Help()`. Local commands whose `Short` carries usage (`audit`, `init`, `migrate`, `register`, `search`, `explore`, `feed`, `dashboard`) get summary-only `Short` strings; their usage stays in `Use` and `Long`.

- [ ] **Step 3: Run**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbay -count=1`
Expected: PASS, including `TestEveryCommandIsReachable`.

- [ ] **Step 4: Commit and open MR 4**

```bash
git add cmd/gitbay
git commit -m "gitbay: summaries generated from the registry; grouped root help" -m "Ref #254"
git push -u origin cli-output-help
gitbay mr create --source cli-output-help --target main --title "CLI output: help from the registry"
```

---

# Part 5: docs (branch `cli-output-docs`)

### Task 5.1: wiki and changelog

**Files:**
- Modify: `.gitbay/wiki/Users.org` ("Output rules"), `.gitbay/wiki/Admin.org`, `CHANGELOG.org`

- [ ] **Step 1: Users.org.** In "Output rules", change the list rule's timestamp wording to "timestamps are RFC3339 to the second, UTC", replace the sentence about the CLI padding tabs, and add after the list:

```org
** At a terminal

The =gitbay= CLI sends =GITBAY_TERM=<cols>[,color]= on the SSH session
when stdout is a terminal. The server then prints:

- lists under a header, padded, fitted to the width (the title or
  description column is cut with =…= first), states in colour, ages as
  =2h ago=, and the next page as a command on stderr;
- =show= views with a title line, aligned fields, the body rendered
  from markdown or org, one line per event, and comments under a rule;
  timestamps as =2026-09-23 23:26 UTC=;
- help with flag descriptions and examples.

=NO_COLOR=, =TERM=dumb= and =--no-color= drop the colour. Views, diffs
and logs go through =$GITBAY_PAGER=, else =$PAGER=, else =less=; an
empty =GITBAY_PAGER= turns it off. Stock ssh gets the plain output
unless it sets the variable: =ssh -o SetEnv=GITBAY_TERM=120,color
git@gitbay.org issue list krz/gitbay=.
```

(If Task 2.3 took Step 6, describe the leading =--term=<cols>[,color]= argument instead of =SetEnv=.)

- [ ] **Step 2: Admin.org.** Where the system-sshd forced command (`gitbayd shell`) is documented, add: "Terminal output needs `AcceptEnv GITBAY_TERM` in `sshd_config`; without it every session gets plain output."

- [ ] **Step 3: CHANGELOG.org.** A new top section headed with the next minor version and the release date, in the style of the entries below it:

```org
* v1.36.0 — <date>

Terminal output for the CLI (#254).

- At a terminal, lists print under a header, fitted to the width, with
  states in colour and relative ages; the next page is a command on
  stderr. Piped output is the same tab-separated rows, with timestamps
  as RFC3339 to the second.
- =show= commands print a title line, aligned fields, the body rendered
  from markdown or org, events one per line and comments under a rule,
  through a pager when longer than the screen.
- Help describes every flag, with examples, and =gitbay --help= groups
  the commands. =help --json= adds =flags= and =examples=.
- =release list= takes =--limit= and =--cursor=, and leaves the title
  empty when it repeats the tag. =notifications device add= prints
  =registered device <n>=. =dashboard= prints =none= under an empty
  section.

Operators running the system-sshd forced command add =AcceptEnv
GITBAY_TERM= to =sshd_config= for terminal output.
```

- [ ] **Step 4: Commit and open MR 5**

```bash
git add .gitbay/wiki/Users.org .gitbay/wiki/Admin.org CHANGELOG.org
git commit -m "docs: terminal output rules, sshd AcceptEnv, changelog" -m "Closes #254"
git push -u origin cli-output-docs
gitbay mr create --source cli-output-docs --target main --title "CLI output: docs and changelog"
```

Tagging, release and deploy follow the usual release steps once this merges.
