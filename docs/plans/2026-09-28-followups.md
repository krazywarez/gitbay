# Review follow-ups implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close #287, #284, #298, #285 and #297: the runner drop-in's
comment names the repositories the runner is really attached to;
`webhook add` takes its signing secret on stdin only; `repo import
--from` fetches only over http(s) from a resolved, checked and pinned
address; an LFS transfer token dies with the SSH key that obtained it;
and a browser session mints credentials only within 15 minutes of
signing in.

**Architecture:** No migrations. Four MRs on independent branches off
`main`. The resolve-check-pin logic mirror sync gained in #279 moves
out of `internal/mirror` into a new `internal/gitpin` package, which
mirror sync and `repo import` both call (`internal/control` cannot
import `internal/mirror`: mirror imports control). LFS tokens gain a
key id in their signed payload, and `httpd.lfsAuth` checks it with
`store.LiveSSHKeys`, the liveness query #256 added. For #297,
`web_sessions.created_at` is the sign-in time (only `/login?token=`
creates a session, and the idle renewal from #276 never writes
`created_at`), so `store.WebSessionUser` returns it on the user,
every web dispatch copies it into a new `Ctx.SignedInAt`, and
`control.runChecked` refuses a `MintsCredential` command when it is
older than `control.ReauthWindow`, next to the #257 `Expires` check.
SSH and the API never set `SignedInAt`, so they are untouched.

**Tech stack:** Go 1.27, SQLite (modernc), `golang.org/x/crypto/ssh`,
cobra, git ≥ 2.37 (`http.curloptResolve`), git-lfs (e2e only).

**Spec:** none — the issue texts of #284, #285, #287, #297 and #298 on
krz/gitbay, and the decisions below, are the spec.

## Global constraints

- Four MRs, each on its own branch off `main`, in a fresh worktree
  (`git worktree add ../gitbay-<branch> -b <branch> main`); the
  checkout may hold another session's edits.
- Commits are signed (the repository refuses unsigned ones); messages
  reference the issue they touch (`Ref #N`), and the commit that
  finishes an issue says `Closes #N`. No attribution to any assistant,
  model or AI anywhere: commits, MR bodies, comments.
- MR: `gitbay mr create --source <branch> --target main --title "..."`;
  merge with `gitbay mr merge <n> --strategy ff` once CI is green
  (this repository requires signed commits, so `squash`/`merge` are
  refused), then delete the branch locally and on the remote. If the
  merge reports the branch is behind, rebase onto `main`, force-push,
  merge again.
- Locally: `go build ./...`, `go vet ./...`, the unit tests of every
  touched package, and at most the one e2e test named in the task
  (`go test ./e2e -run TestName -count=1`). CI on bay1 runs the full
  suite.
- No new migrations; the next free number stays 0067.
- Registries that fail CI when a new thing lacks its row: a top-level
  route word in `internal/policy/names.go`; a new page template in the
  width map of `TestMainWidthClass` (`internal/web/web_test.go`); a new
  `ReadOnly` command in `readArgs` in `e2e/readonly_test.go`; a new
  control command needs a `pass()` entry in `cmd/gitbay/main.go`
  (`cmd/gitbay/coverage_test.go`); a command that reads stdin needs
  `ReadsStdin: true`; a changed `Summary` needs
  `go test ./cmd/gitbay -run TestSummariesAreCurrent -update`
  (`cmd/gitbay/summaries_gen.go`). This plan adds no route, no
  template, no command and changes no summary; it sets `ReadsStdin` on
  `webhook add` (Task 1.2).
- Secrets travel on stdin, never argv; never logged or echoed.
- Wiki pages live in `.gitbay/wiki/`. Update the page in the MR that
  changes the behaviour it describes; close the matching row in
  `Architecture/10-Known-Gaps.org` and update the row in
  `Architecture/09-Controls.org`. Parity is not affected by any of the
  four MRs (webhooks have no web form; no capability changes surface).
- `CHANGELOG.org`: each MR's last task appends its entries at the end
  of the `* Unreleased` bullet list, directly above
  `* v1.36.0 — 2026-09-23`.
- Writing style: plain, direct, no hype; code comments match the
  surrounding density; no before/after narration in comments or docs.

## Decisions (binding, from the brief)

- #287: correct the comment in `deploy/gitbay-runner.override.conf`:
  `cmc/ci-smoke` no longer exists; the runner is attached to krz/gitbay,
  krz/hutch, krz/keycask, krz/orgo, krz/skunky-art and cmc/cleberg.net;
  a scratch repository is created and attached for runner validation.
- #284: `webhook add` takes the secret from stdin only (`--secret -`,
  `ReadsStdin: true`); a literal `--secret <value>` is refused with a
  message saying to pipe it, as `repo secret set` does.
- #298: `repo import --from` accepts http and https only; `git://` and
  anything else is refused with a message to use the https URL. Import
  gets the same resolve, check and pin as mirror sync, through one
  shared helper.
- #285: LFS transfer tokens carry the SSH key id (deploy keys
  included); each LFS request that presents a token checks the key
  still exists, is unexpired, and its account is not disabled. Tokens
  minted before the upgrade (no key id) are refused.
- #297: a `MintsCredential` command dispatched from a web session is
  refused unless the session signed in within the last 15 minutes; the
  web shows the form again with the message and a "Sign in again" link
  that returns to the form. The check lives in `control.runChecked`.

## Findings from the code that shape this plan

- `repo secret set` takes no value argument at all
  (`internal/control/build.go:426-451`): a third argument is a usage
  error, and an empty stdin is refused with
  `no value on stdin (pipe it: printf %s TOKEN | ...)`. `webhook add`
  mirrors both refusals, as exit 2.
- No web handler builds `webhook add` (Parity: webhooks, web `no`), so
  #284 changes only the command, the CLI passthrough, one e2e call
  (`e2e/webhook_test.go:115-116`) and `API.org:153`.
- The CLI forwards stdin for a `stdinOK` command only when `usesStdin`
  (`cmd/gitbay/main.go:358-368`) sees `--file -`, `--key -` or
  `--token-stdin`; it must learn `--secret -`.
- Deploy keys are `ssh_keys` rows with scope `deploy:<repo>:ro|rw`
  (`internal/policy/access.go:60-79`). `git-lfs-authenticate` is
  reached from `sshd.Exec` (`internal/sshd/sshd.go:492-499`), which
  holds the authenticating `store.SSHKey`, so its `ID` identifies user
  keys and deploy keys alike, and `store.LiveSSHKeys`
  (`internal/store/revoke.go:36-62`) already answers "registered,
  unexpired, account not disabled" for both.
- `lfsBatch` (`internal/httpd/lfs.go:130`) mints transfer tokens for
  anonymous batch requests on public repositories too. Those carry key
  id 0, and `lfsAuth` honours a key-0 token only as an anonymous
  download of a public repository.
- A pre-upgrade LFS token's payload has three fields
  (`repo:op:exp`, `internal/lfs/lfs.go:130`); the new payload has four,
  so `Verify` refuses the old shape by field count. The server returns
  `expires_in` with every token; nothing in `internal/lfs` makes a
  client retry after a refusal. git-lfs obtains a token per process
  from `git-lfs-authenticate`, so only a transfer in flight across the
  deploy fails; the upgrade note says so.
- `web_sessions.created_at` defaults to insert time
  (`0001_init.up.sql:192-197`), `CreateWebSession` is called only from
  `/login?token=` (`internal/httpd/accounts.go:153`), and
  `WebSessionUser`'s renewal updates `last_used_at` and `expires_at`
  only (`internal/store/sessions.go:98-100`). `created_at` is the
  sign-in time; no column is needed.
- Web dispatch builds a `control.Ctx` in five places
  (`internal/httpd/control.go:35, 59, 105, 147, 188`), each from a
  `store.User` and nothing else. Carrying the sign-in time on the user
  that `viewer` returns reaches all five without changing a handler
  signature; one `webCtx` helper replaces the five literals so none can
  miss the field.
- Web forms that dispatch a `MintsCredential` command:
  `keys add`, `email verify`, `token create` (`/settings`,
  `internal/httpd/account.go:224, 277, 309`) and `repo runner add`
  (repository settings, `internal/httpd/settings.go:193`). The account
  page reports a refusal through the flash cookie and a redirect; the
  repository settings page re-renders the form with the notice.
- `setNext` (`internal/httpd/flash.go:52`) sets the `gitbay_next`
  cookie that `/login?token=` follows after creating a session
  (`accounts.go:158`); the login page names the destination.
- `repo import`'s unit-level harness: `newQueueTestRepo` and
  `pruneCtx` (`internal/control/build_test.go:49`,
  `mrprune_test.go:68`). `pruneCtx` leaves `Limits.CloneTimeoutSec`
  at 0, which is an already-expired context; the import tests set it.
- `TestRepoImport` (`e2e/import_test.go`) imports from its own
  instance at `127.0.0.1` on a default instance and imports once over
  `git://`. Both stop working: it moves to `allow_local = true`, and
  the `git://` import becomes a refusal case.
- `deploy/gitbay-runner.override.conf:35-39` and `Admin.org:923-937,
  953-954` both describe `cmc/ci-smoke` and a `-repos krz/gitbay` on
  `ExecStart` that the unit no longer has (`override.conf:87`); the
  Admin procedure's `sed` matches nothing.

## Order and dependencies

| # | Branch | Closes | Migration | Depends on |
|---|---|---|---|---|
| 1 | `webhook-secret-stdin` | #287, #284 | — | — |
| 2 | `import-pin-address` | #298 | — | — |
| 3 | `lfs-token-key` | #285 | — | — |
| 4 | `web-mint-reauth` | #297 | — | — |

#287 and #284 share MR 1: #287 is a comment and a wiki procedure, too
small for its own review, and neither touches a file the other does.
No MR changes code another MR changes; none is stacked. Overlaps are
textual only and resolve on rebase by keeping both sides:

- Every MR appends to `* Unreleased` in `CHANGELOG.org`.
- MRs 2, 3 and 4 edit `Threat-Model.org` and
  `Architecture/09-Controls.org` (different paragraphs and rows); MRs
  2 and 4 each delete their own row from `Architecture/10-Known-Gaps.org`;
  MRs 3 and 4 edit different rows of `Architecture/05-Identity-and-Access.org`.

Land in the table's order; any order works.

## File map

| File | MR | Responsibility |
|---|---|---|
| `deploy/gitbay-runner.override.conf` | 1 | comment names the real attachments |
| `.gitbay/wiki/Admin.org` | 1, 2 | scratch-repository procedure; import in `[webhooks]`/`[limits]` |
| `internal/control/webhook.go`, `webhook_test.go` | 1 | `--secret -` from stdin, literal refused |
| `cmd/gitbay/main.go`, `stdinpayload_test.go` | 1 | forward stdin for `--secret -` |
| `e2e/webhook_test.go` | 1 | pipe the secret |
| `.gitbay/wiki/API.org` | 1 | webhook add usage |
| `internal/gitpin/gitpin.go`, `gitpin_test.go` | 2 | resolve, check, pin args, clean env, git version |
| `internal/mirror/mirror.go`, `mirror_test.go` | 2 | sync through `gitpin` |
| `internal/gitutil/gitutil.go` | 2 | `FetchMirror`, `RemoteDefaultBranch` take pin args and a whole env |
| `internal/control/import.go`, `import_test.go` | 2 | http(s) only, resolve, check, pin |
| `e2e/import_test.go` | 2 | `allow_local`, `git://` refused |
| `.gitbay/wiki/Users.org`, `Architecture/02-Components.org` | 2 | import rules; `internal/gitpin` row |
| `internal/lfs/lfs.go`, `lfs_test.go` | 3 | key id in the token, `Grant` |
| `internal/sshd/lfs.go`, `internal/sshd/sshd.go` | 3 | sign with the authenticating key |
| `internal/httpd/lfs.go`, `lfsauth_test.go` | 3 | liveness check, key-0 rule |
| `e2e/lfs_test.go` | 3 | `TestLFSTokenEndsWithItsKey` |
| `.gitbay/wiki/Architecture/04-Trust-Boundaries.org` | 3 | LFS flow |
| `internal/store/users.go`, `sessions.go`, `sessions_test.go` | 4 | `User.SignedInAt` |
| `internal/control/control.go`, `reauth_test.go` | 4 | `SignedInAt`, `ReauthWindow`, `ReauthRefusal`, the check |
| `internal/httpd/control.go`, `flash.go`, `account.go`, `settings.go`, `account_test.go`, `reauth_test.go` | 4 | `webCtx`, the sign-in link |
| `internal/web/templates/account.html`, `settings.html` | 4 | the link |
| `.gitbay/wiki/Threat-Model.org`, `Architecture/05-Identity-and-Access.org`, `09-Controls.org`, `10-Known-Gaps.org` | 2, 3, 4 | docs per MR |
| `CHANGELOG.org` | 1–4 | entries under `* Unreleased` |

---

# MR 1: runner comment and webhook secret on stdin (branch `webhook-secret-stdin`, closes #287 and #284)

### Task 1.1: the runner drop-in and the Admin procedure name what exists

**Files:**
- Modify: `deploy/gitbay-runner.override.conf:35-39`
- Modify: `.gitbay/wiki/Admin.org:923-937`, `:953-954`

**Interfaces:** none.

- [ ] **Step 1: Correct the drop-in's comment**

Replace lines 35-39:

```
# The runner polls as a non-admin account with a runner-scoped key, and
# claims only the repositories that key is attached to (`repo runner
# add`): krz/gitbay and cmc/ci-smoke. The attachments are the boundary,
# so ExecStart names no -repos. cmc/ci-smoke is the nightly isolation
# canary; keep it attached or its scheduled build waits forever.
```

with:

```
# The runner polls as a non-admin account with a runner-scoped key, and
# claims only the repositories that key is attached to (`repo runner
# add`): krz/gitbay, krz/hutch, krz/keycask, krz/orgo, krz/skunky-art
# and cmc/cleberg.net. The attachments are the boundary, so ExecStart
# names no -repos. To validate a runner change, create a scratch
# repository, attach this key to it, and run with -repos naming only
# that repository until the change is proven (Admin wiki, CI runner).
```

- [ ] **Step 2: Correct the Admin procedure**

In `.gitbay/wiki/Admin.org`, replace lines 923-937 (from
`*Validate podman mode on a scratch repository` through `#+end_src`)
with:

```org
*Validate podman mode on a scratch repository before pointing the runner
at real ones.* Every deploy that switched the whole instance to
containers and failed took CI down with it. Instead: create a throwaway
repository the runner account can read (public, or granted read — a
private one is "not found" to the runner and the build stays pending),
give it one job that names the CI image, attach the runner's key to it,
and deploy the runner with =-repos= naming only that repository. The
production unit, with its real hardening, then claims nothing else;
other repositories' builds queue until =-repos= is removed again, which
is a pause, not an outage.

#+begin_src sh
gitbay repo create cmc/runner-scratch   # then push a .gitbay/ci.yml naming the image
# on the host:
gitbay repo runner add cmc/runner-scratch < /var/lib/gitbay-runner/.ssh/id_ed25519.pub
sed -i 's#^ExecStart=/usr/local/bin/gitbay-runner #&-repos cmc/runner-scratch #' /etc/systemd/system/gitbay-runner.service.d/override.conf
systemctl daemon-reload && systemctl restart gitbay-runner
gitbay build log cmc/runner-scratch 1   # green: remove -repos, redeploy, delete the scratch repository
#+end_src
```

Delete lines 953-954:

```org
The nightly canary on =cmc/ci-smoke= only runs if the runner's =-repos=
names that repository too; a scoped runner claims nothing else.
```

- [ ] **Step 3: Check nothing else names the old repository**

Run: `grep -rn "ci-smoke" --exclude-dir=.git . | grep -v "docs/plans\|docs/specs\|CHANGELOG"`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add deploy/gitbay-runner.override.conf .gitbay/wiki/Admin.org
git commit -S -m "deploy: runner comment and Admin procedure name the real attachments

cmc/ci-smoke no longer exists and ExecStart carries no -repos; the
scratch-repository check attaches the key and adds -repos for the run.

Closes #287"
```

### Task 1.2: `webhook add` reads the secret from stdin

**Files:**
- Modify: `internal/control/webhook.go:1-13` (imports), `:16-24` (registration), `:47-81` (`runWebhookAdd`)
- Test: `internal/control/webhook_test.go`

**Interfaces:**
- Produces: `webhook add <owner/name> <url> [--secret -] [--events ...]`, `ReadsStdin: true`. Refusal texts, exit 2:
  `the secret is read from stdin, never argv: pipe it and pass --secret - (printf %s SECRET | ... --secret -)` and
  `no secret on stdin (pipe it: printf %s SECRET | ... --secret -)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/control/webhook_test.go`:

```go
// The signing secret arrives on stdin with --secret -, like a build
// secret: a value on the command line is refused before anything is
// stored, since argv shows in /proc and in shell history (#284).
func TestWebhookAddSecretFromStdin(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	run := func(stdin string, argv ...string) (string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		c.Cfg.Limits.WriteRate = -1
		c.Cfg.Webhooks.AllowLocal = true
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return errOut.String(), code
	}
	msg, code := run("", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "s3cret")
	if code != protocol.ExitUsage || !strings.Contains(msg, "--secret -") {
		t.Fatalf("literal secret: exit %d, %q", code, msg)
	}
	if msg, code := run("", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "-"); code != protocol.ExitUsage || !strings.Contains(msg, "no secret on stdin") {
		t.Fatalf("empty stdin: exit %d, %q", code, msg)
	}
	if hooks, err := st.ListWebhooks(repo.ID); err != nil || len(hooks) != 0 {
		t.Fatalf("a refused add stored %+v (%v)", hooks, err)
	}
	if msg, code := run("s3cret\n", "webhook", "add", repo.Path(), "http://127.0.0.1/hook", "--secret", "-"); code != protocol.ExitOK {
		t.Fatalf("piped secret: exit %d, %q", code, msg)
	}
	// Without --secret nothing reads stdin and the hook is unsigned.
	if msg, code := run("not a secret\n", "webhook", "add", repo.Path(), "http://127.0.0.1/other"); code != protocol.ExitOK {
		t.Fatalf("no secret: exit %d, %q", code, msg)
	}
	hooks, err := st.ListWebhooks(repo.ID)
	if err != nil || len(hooks) != 2 {
		t.Fatalf("hooks: %+v %v", hooks, err)
	}
	if hooks[0].Secret != "s3cret" || hooks[1].Secret != "" {
		t.Fatalf("secrets: %q, %q", hooks[0].Secret, hooks[1].Secret)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestWebhookAddSecretFromStdin -count=1`
Expected: FAIL at `literal secret: exit 0` (the literal is accepted today).

- [ ] **Step 3: Implement**

Imports in `internal/control/webhook.go` gain `"strings"`:

```go
import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/webhook"
)
```

The registration:

```go
	register(Command{Path: []string{"webhook", "add"},
		Summary: "add a webhook",
		Usage:   "webhook add <owner/name> <url> [--secret -] [--events push,issue.created|*]",
		Flags: []Flag{
			{"--secret", "-", "read the secret that signs deliveries from stdin", ""},
			{"--events", "push,issue.created|*", "which events to send", "*"},
		},
		Examples: []string{
			"webhook add krz/gitbay https://ci.example.org/hook --events push",
			"webhook add krz/gitbay https://ci.example.org/hook --secret - < secret.txt",
		},
		ReadsStdin: true,
		Run:        runWebhookAdd})
```

`runWebhookAdd`, whole function:

```go
func runWebhookAdd(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--secret", "--events"}, MaxPos: 2, Usage: "webhook add <owner/name> <url> [--secret -] [--events push,issue.created|*]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, url, events := f.pos(0), f.pos(1), "*"
	if f.Has("--events") {
		events = f.Value("--events")
	}
	if path == "" || url == "" {
		return c.usage()
	}
	// Secrets travel on stdin: argv shows in /proc and in shell history.
	if f.Has("--secret") && f.Value("--secret") != "-" {
		return c.fail(protocol.ExitUsage, "the secret is read from stdin, never argv: pipe it and pass --secret - (printf %%s SECRET | ... --secret -)")
	}
	repo, code := resolveRepo(c, path, policy.CanAdmin)
	if code >= 0 {
		return code
	}
	// A name that is not an event is a subscription that never fires, and
	// nothing would ever say so. Checked before the URL, which resolves
	// DNS: a typo here should not need a reachable host to report.
	if code := checkEventNames(c, events); code >= 0 {
		return code
	}
	if err := webhook.ValidateURL(url, c.Cfg.Webhooks.AllowLocal); err != nil {
		// The command line parsed; the value is what the server refuses.
		// Exit 1 carries the reason to every client verbatim (#187).
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	secret := ""
	if f.Has("--secret") {
		raw, err := io.ReadAll(io.LimitReader(c.Stdin, 64<<10))
		if err != nil {
			return c.fail(protocol.ExitFailure, "reading secret: %v", err)
		}
		secret = strings.TrimRight(string(raw), "\n")
		if secret == "" {
			return c.fail(protocol.ExitUsage, "no secret on stdin (pipe it: printf %%s SECRET | ... --secret -)")
		}
	}
	id, err := c.Store.AddWebhook(repo.ID, url, secret, events)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"id": id, "url": url, "events": events}, func(w io.Writer) {
		fmt.Fprintf(w, "webhook %d added for %s (%s)\n", id, repo.Path(), events)
	})
}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/control -count=1`
Expected: PASS, including `TestWebhookAddRefusedURLIsAFailure`,
`TestStdinCommandsReadStdin` and the help tests (the usage names
`--secret`, the flag is described, both examples resolve to
`webhook add`).

- [ ] **Step 5: Commit**

```bash
git add internal/control/webhook.go internal/control/webhook_test.go
git commit -S -m "webhook: add reads the signing secret from stdin

--secret - reads it; a value on the command line is refused.

Ref #284"
```

### Task 1.3: the CLI forwards stdin for `--secret -`

**Files:**
- Modify: `cmd/gitbay/main.go:358-368` (`usesStdin`), `:753` (`webhook add` pass)
- Test: `cmd/gitbay/stdinpayload_test.go`

**Interfaces:**
- Consumes: `webhook add ... --secret -` (Task 1.2).

- [ ] **Step 1: Write the failing test**

Append to `cmd/gitbay/stdinpayload_test.go`:

```go
// webhook add --secret - reads the secret on the server, so the CLI must
// forward stdin for it the way it does for --file - (#284).
func TestUsesStdinForSecretDash(t *testing.T) {
	if !usesStdin([]string{"alice/app", "https://ci.example/hook", "--secret", "-"}) {
		t.Error("--secret - does not forward stdin")
	}
	if usesStdin([]string{"alice/app", "https://ci.example/hook", "--events", "push"}) {
		t.Error("forwarded stdin with no flag asking for it")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./cmd/gitbay -run TestUsesStdinForSecretDash -count=1`
Expected: FAIL, `--secret - does not forward stdin`.

- [ ] **Step 3: Implement**

`usesStdin`:

```go
// usesStdin reports whether the arguments request stdin content.
func usesStdin(args []string) bool {
	for i, a := range args {
		if (a == "--file" || a == "--key" || a == "--secret") && i+1 < len(args) && args[i+1] == "-" {
			return true
		}
		if a == "--token-stdin" {
			return true
		}
	}
	return false
}
```

`cmd/gitbay/main.go:753`:

```go
		pass("add", passOpts{server: []string{"webhook", "add"}, needsRepo: true, stdinOK: true, stdinWhat: "the webhook secret", stdinSecret: true}),
```

- [ ] **Step 4: Run the package**

Run: `go test ./cmd/gitbay -count=1 && go vet ./cmd/gitbay`
Expected: PASS (coverage, summaries and stdin-payload tests included).

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbay/main.go cmd/gitbay/stdinpayload_test.go
git commit -S -m "cli: webhook add forwards stdin for --secret -

Ref #284"
```

### Task 1.4: e2e, API wiki and changelog

**Files:**
- Modify: `e2e/webhook_test.go:115-116`
- Modify: `.gitbay/wiki/API.org:153` and the paragraph after its `#+end_src`
- Modify: `CHANGELOG.org` (`* Unreleased`)

**Interfaces:** none.

- [ ] **Step 1: Pipe the secret in the e2e test**

`e2e/webhook_test.go:115-116`:

```go
	if _, errOut, code := inst.ssh(t, aliceKey, "s3cret\n",
		"webhook", "add", "alice/proj", hookURL, "--secret", "-"); code != 0 {
```

The HMAC check below it (`hmac.New(sha256.New, []byte("s3cret"))`)
stays: the server trims the trailing newline.

- [ ] **Step 2: Run the e2e test**

Run: `go build ./... && go test ./e2e -run 'TestWebhooks$' -count=1`
Expected: PASS.

- [ ] **Step 3: API wiki**

`.gitbay/wiki/API.org:153` becomes:

```org
printf %s "$SECRET" | gitbay webhook add <url> --secret - [--events push,issue.created]  # default *
```

After that block's `#+end_src` and before `** Events`, add:

```org

The signing secret is read from stdin with =--secret -=; a value on the
command line is refused, since argv shows in process listings and shell
history. Over the JSON API it goes in the request's =stdin= field.
```

- [ ] **Step 4: Changelog**

Append to the `* Unreleased` list, directly above `* v1.36.0 — 2026-09-23`:

```org
- =webhook add= reads the signing secret from stdin with =--secret -=;
  a value on the command line is refused, since argv shows in process
  listings and shell history. A script that passed the value must pipe
  it: =printf %s "$SECRET" | gitbay webhook add <repo> <url> --secret -=
  (#284).
```

- [ ] **Step 5: Commit, MR, merge**

```bash
git add e2e/webhook_test.go .gitbay/wiki/API.org CHANGELOG.org
git commit -S -m "webhook: document --secret -, pipe it in the e2e test

Closes #284"
git push -u origin webhook-secret-stdin
gitbay mr create --source webhook-secret-stdin --target main --title "webhook add reads its secret from stdin; runner comment names the real attachments"
```

Merge `--strategy ff` after CI, delete the branch both places.

---

# MR 2: import checks and pins its address (branch `import-pin-address`, closes #298)

### Task 2.1: `internal/gitpin`

**Files:**
- Create: `internal/gitpin/gitpin.go`
- Create: `internal/gitpin/gitpin_test.go`

**Interfaces:**
- Produces:
  - `type Lookup func(ctx context.Context, host string) ([]net.IP, error)`
  - `func LookupIP(ctx context.Context, host string) ([]net.IP, error)`
  - `type Remote struct { URL *url.URL; IPs []net.IP }`
  - `func Resolve(ctx context.Context, lookup Lookup, raw string, allowLocal bool) (Remote, error)`
  - `func (r Remote) Args() []string`
  - `func Env(home string) []string`
  - `func VersionOK(out string) error`
  - `func CheckGit(ctx context.Context) error`

- [ ] **Step 1: Write the failing tests**

`internal/gitpin/gitpin_test.go`:

```go
package gitpin

import (
	"context"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func answer(ips ...string) Lookup {
	return func(context.Context, string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	r, err := Resolve(ctx, answer("203.0.113.5"), "https://git.example/x.git", false)
	if err != nil || r.URL.Hostname() != "git.example" || len(r.IPs) != 1 {
		t.Fatalf("public: %+v %v", r, err)
	}
	if _, err := Resolve(ctx, answer("203.0.113.5", "10.0.0.7"), "https://git.example/x.git", false); err == nil || !strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("private: %v", err)
	}
	if _, err := Resolve(ctx, answer("10.0.0.7"), "https://git.example/x.git", true); err != nil {
		t.Fatalf("allow_local: %v", err)
	}
	// An empty resolve list would leave curl to resolve the host itself.
	if _, err := Resolve(ctx, answer(), "https://git.example/x.git", true); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("empty answer: %v", err)
	}
	for _, raw := range []string{"git://git.example/x.git", "ssh://git.example/x.git", "file:///etc"} {
		_, err := Resolve(ctx, func(context.Context, string) ([]net.IP, error) {
			t.Fatalf("looked up a host for %s", raw)
			return nil, nil
		}, raw, true)
		if err == nil || !strings.Contains(err.Error(), "not http or https") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestArgs(t *testing.T) {
	u, _ := url.Parse("https://git.example/x.git")
	got := Remote{u, []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("2001:db8::1")}}.Args()
	want := []string{"-c", "http.followRedirects=false",
		"-c", "http.curloptResolve=git.example:443:203.0.113.5,[2001:db8::1]"}
	if !slices.Equal(got, want) {
		t.Fatalf("https: %q", got)
	}
	u, _ = url.Parse("http://git.example:8080/x.git")
	if got := (Remote{u, []net.IP{net.ParseIP("203.0.113.5")}}).Args(); got[3] != "http.curloptResolve=git.example:8080:203.0.113.5" {
		t.Fatalf("http with port: %q", got)
	}
	// An address literal is its own resolution; there is nothing to pin.
	u, _ = url.Parse("https://203.0.113.5/x.git")
	if got := (Remote{u, []net.IP{net.ParseIP("203.0.113.5")}}).Args(); !slices.Equal(got, []string{"-c", "http.followRedirects=false"}) {
		t.Fatalf("literal: %q", got)
	}
}

func TestEnv(t *testing.T) {
	want := []string{"GIT_TERMINAL_PROMPT=0", "HOME=/srv/gitbay",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if got := Env("/srv/gitbay"); !slices.Equal(got, want) {
		t.Fatalf("Env = %q", got)
	}
}

func TestVersionOK(t *testing.T) {
	for _, s := range []string{"git version 2.37.0", "git version 2.47.3", "git version 2.39.5 (Apple Git-154)",
		"git version 2.45.2.windows.1", "git version 3.0.0\n"} {
		if err := VersionOK(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"git version 2.36.9", "git version 1.99.0", "git version 2", "nonsense", ""} {
		if err := VersionOK(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/gitpin -count=1`
Expected: build failure, `undefined: Lookup` / `undefined: Resolve`.

- [ ] **Step 3: Implement**

`internal/gitpin/gitpin.go`:

```go
// Package gitpin runs git against a user-supplied http or https remote
// only at addresses resolved and checked immediately before: mirror
// sync (#279) and repo import (#298).
package gitpin

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
	"gitbay.org/gitbay/internal/webhook"
)

// Lookup resolves a host to its addresses.
type Lookup func(ctx context.Context, host string) ([]net.IP, error)

// LookupIP is the system resolver.
func LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// Remote is a URL whose host resolved to IPs, every one of which passed
// the address check.
type Remote struct {
	URL *url.URL
	IPs []net.IP
}

// Resolve parses raw, requires http or https, resolves the host with
// lookup, and refuses it when it resolves to nothing or, unless
// allowLocal, to any private or local address.
func Resolve(ctx context.Context, lookup Lookup, raw string, allowLocal bool) (Remote, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Remote{}, err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Remote{}, fmt.Errorf("URL scheme %q is not http or https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return Remote{}, fmt.Errorf("URL has no host")
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return Remote{}, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		// An empty resolve list would leave curl to resolve the host itself.
		return Remote{}, fmt.Errorf("%s resolves to no address", host)
	}
	if err := webhook.CheckAddrs(host, ips, allowLocal); err != nil {
		return Remote{}, err
	}
	return Remote{URL: u, IPs: ips}, nil
}

// Args are git's leading -c options for r: curl's resolve list pins
// the host to the checked addresses, and with redirects off a server
// cannot send git on to a host nobody checked. An address literal
// needs no pin.
func (r Remote) Args() []string {
	args := []string{"-c", "http.followRedirects=false"}
	host := r.URL.Hostname()
	if net.ParseIP(host) != nil {
		return args
	}
	port := r.URL.Port()
	if port == "" {
		port = "443"
		if r.URL.Scheme == "http" {
			port = "80"
		}
	}
	addrs := make([]string, len(r.IPs))
	for i, ip := range r.IPs {
		if ip.To4() == nil {
			addrs[i] = "[" + ip.String() + "]"
		} else {
			addrs[i] = ip.String()
		}
	}
	return append(args, "-c", "http.curloptResolve="+host+":"+port+":"+strings.Join(addrs, ","))
}

// Env is git's whole environment for a pinned remote. No system or
// global gitconfig: a proxy, URL rewrite or redirect setting there
// would take git around the pin.
func Env(home string) []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

// VersionOK accepts the output of `git version` for git 2.37 or later,
// the first release with http.curloptResolve. An older git ignores the
// setting and would resolve the host itself.
func VersionOK(out string) error {
	fields := strings.Fields(out)
	if len(fields) >= 3 && fields[0] == "git" && fields[1] == "version" {
		parts := strings.Split(fields[2], ".")
		if len(parts) >= 2 {
			major, err1 := strconv.Atoi(parts[0])
			minor, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				if major > 2 || major == 2 && minor >= 37 {
					return nil
				}
				return fmt.Errorf("git %s is older than 2.37 and cannot pin remote addresses", fields[2])
			}
		}
	}
	return fmt.Errorf("cannot read git version from %q", strings.TrimSpace(out))
}

// CheckGit runs the server's git and refuses one that cannot pin.
func CheckGit(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, toolpath.Look("git"), "version").Output()
	if err != nil {
		return fmt.Errorf("running git version: %v", err)
	}
	return VersionOK(string(out))
}
```

`webhook` imports only `store`, and `store` imports neither `gitpin`
nor `webhook`: no cycle.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/gitpin -count=1 && go vet ./internal/gitpin`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitpin
git commit -S -m "gitpin: resolve, check and pin a git remote

The mirror worker's resolve-check-pin, as a package import can share.

Ref #298"
```

### Task 2.2: mirror sync goes through `gitpin`

**Files:**
- Modify: `internal/mirror/mirror.go:8-26` (imports), `:46-79` (`New`, `Run`), `:102-167` (`sync`); delete `:169-215` (`pinArgs`, `gitVersionOK`)
- Modify: `internal/mirror/mirror_test.go:3-19` (imports), `:141-160` (`TestSweepRefusesWithAnOldGit`); delete `:162-176` (`TestGitVersionOK`) and `:242-260` (`TestPinArgs`)

**Interfaces:**
- Consumes: `gitpin.LookupIP`, `gitpin.Resolve`, `gitpin.Remote.Args`, `gitpin.Env`, `gitpin.VersionOK`, `gitpin.CheckGit` (Task 2.1).
- Produces: no new names; `Worker.Lookup` keeps its type.

- [ ] **Step 1: Point the old-git test at the moved function**

In `TestSweepRefusesWithAnOldGit`, replace

```go
	w.gitErr = gitVersionOK("git version 2.36.1")
```

with

```go
	w.gitErr = gitpin.VersionOK("git version 2.36.1")
```

Delete `TestGitVersionOK` and `TestPinArgs` (now `gitpin`'s
`TestVersionOK` and `TestArgs`). Add
`"gitbay.org/gitbay/internal/gitpin"` to the test imports.

- [ ] **Step 2: Baseline**

This task is a refactor: the mirror tests are the check, green before
and after.

Run: `go test ./internal/mirror -count=1`
Expected: PASS (`gitpin.VersionOK` exists from Task 2.1; the package's
own `pinArgs` and `gitVersionOK` still exist until Step 3).

- [ ] **Step 3: Implement**

Imports:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitpin"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/toolpath"
)
```

`New` sets `Lookup: gitpin.LookupIP` in place of the inline closure:

```go
	return &Worker{St: st, Cfg: cfg, Tick: tick, Lookup: gitpin.LookupIP}
```

`Run`, the first lines through the `slog.Error`:

```go
func (w *Worker) Run(ctx context.Context) {
	if err := gitpin.CheckGit(ctx); err != nil {
		w.gitErr = fmt.Errorf("mirrors disabled: %w", err)
		slog.Error("mirror: not syncing", "err", w.gitErr)
	}
	t := time.NewTicker(w.Tick)
```

`sync`, whole function:

```go
func (w *Worker) sync(m store.Mirror) error {
	repo, err := w.St.RepoByID(m.RepoID)
	if err != nil {
		return err
	}
	dir := control.RepoDir(w.Cfg.Server.Root, repo.OwnerName, repo.Name)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// The URL was checked when saved, but DNS can answer differently
	// now. Check what it resolves to at sync time, then let git connect
	// to exactly those addresses.
	remote, err := gitpin.Resolve(ctx, w.Lookup, m.URL, w.Cfg.Webhooks.AllowLocal)
	if err != nil {
		return err
	}

	env := gitpin.Env(w.Cfg.Server.Root)
	if m.Token != "" {
		askpass := filepath.Join(w.Cfg.Server.Root, "mirror-askpass.sh")
		if err := os.WriteFile(askpass, []byte(askpassScript), 0o700); err != nil {
			return err
		}
		user := m.Username
		if user == "" {
			user = "x-access-token"
		}
		env = append(env,
			"GIT_ASKPASS="+askpass,
			"GITBAY_MIRROR_USER="+user,
			"GITBAY_MIRROR_TOKEN="+m.Token)
	}

	args := append(remote.Args(), "-C", dir)
	if m.Direction == "push" {
		// Branches and tags only: internal refs (merge-requests) stay home.
		args = append(args, "push", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	} else {
		args = append(args, "fetch", "--prune", m.URL,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	}
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %.300s", m.Direction, err, out)
	}
	return nil
}
```

Delete `pinArgs` and `gitVersionOK`.

- [ ] **Step 4: Run the mirror tests and the mirror e2e**

Run: `go test ./internal/mirror ./internal/gitpin -count=1 && go vet ./internal/mirror`
Expected: PASS. `TestSyncRefusesANonHTTPScheme` matches
`not http or https`, `TestSyncRefusesAnEmptyAnswer` matches
`no address`, `TestSweepRefusesWithAnOldGit` matches `2.37`.

Run: `go build ./... && go test ./e2e -run TestMirrors -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mirror
git commit -S -m "mirror: sync through gitpin

Ref #298"
```

### Task 2.3: `repo import` accepts http(s) only and pins the address

**Files:**
- Modify: `internal/gitutil/gitutil.go:218-233` (`FetchMirror`), `:256-273` (`RemoteDefaultBranch`)
- Modify: `internal/control/import.go:3-16` (imports), `:80-116`, `:146-158`
- Create: `internal/control/import_test.go`

**Interfaces:**
- Consumes: `gitpin.CheckGit`, `gitpin.Resolve`, `gitpin.Remote.Args`, `gitpin.Env`, `gitpin.Lookup`, `gitpin.LookupIP` (Task 2.1).
- Produces:
  - `func FetchMirror(ctx context.Context, dir, url string, errW io.Writer, pin, env []string) error`
  - `func RemoteDefaultBranch(ctx context.Context, url string, pin, env []string) (string, error)`
  - `var importLookup gitpin.Lookup = gitpin.LookupIP` (package `control`; tests replace it)
  - Refusal: `import fetches over http:// and https:// only; use the repository's https:// URL` (exit 2).

- [ ] **Step 1: Write the failing tests**

`internal/control/import_test.go`:

```go
package control

import (
	"bytes"
	"context"
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func importCtx(t *testing.T, allowLocal bool) (*Ctx, *bytes.Buffer, *store.Store, string) {
	t.Helper()
	st, _, uid := newQueueTestRepo(t)
	root := t.TempDir()
	c, errOut := pruneCtx(st, root, store.User{ID: uid, Username: "alice"})
	c.Cfg.Limits.WriteRate = -1
	c.Cfg.Limits.CloneTimeoutSec = 60
	c.Cfg.Webhooks.AllowLocal = allowLocal
	c.Stdin = strings.NewReader("")
	return c, errOut, st, root
}

// importUpstream serves a bare repository with one commit on main over
// smart HTTP and returns its URL and that commit.
func importUpstream(t *testing.T) (string, string) {
	t.Helper()
	git := gitRunner(t)
	parent := t.TempDir()
	bare := filepath.Join(parent, "remote.git")
	work := filepath.Join(parent, "work")
	git(parent, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(parent, "init", "-q", "--initial-branch=main", work)
	git(work, "commit", "-q", "--allow-empty", "-m", "one")
	git(work, "push", "-q", bare, "main")
	sha := strings.TrimSpace(git(work, "rev-parse", "HEAD"))
	execPath := strings.TrimSpace(git(parent, "--exec-path"))
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/remote.git", sha
}

// git:// cannot be held to a checked address, so import refuses it
// before creating anything (#298).
func TestRepoImportRefusesGitScheme(t *testing.T) {
	c, errOut, st, _ := importCtx(t, true)
	code := Dispatch(c, []string{"repo", "import", "alice/x", "--from", "git://example.org/x.git"})
	if code != protocol.ExitUsage || !strings.Contains(errOut.String(), "use the repository's https:// URL") {
		t.Fatalf("exit %d, %q", code, errOut.String())
	}
	if _, err := st.RepoByPath("alice/x"); err == nil {
		t.Fatal("a refused import created a repository")
	}
}

// A source on loopback is refused on a default instance, and nothing is
// left behind.
func TestRepoImportRefusesALocalAddress(t *testing.T) {
	c, errOut, st, root := importCtx(t, false)
	code := Dispatch(c, []string{"repo", "import", "alice/x", "--from", "http://127.0.0.1:9/x.git"})
	if code != protocol.ExitFailure || !strings.Contains(errOut.String(), "127.0.0.1") {
		t.Fatalf("exit %d, %q", code, errOut.String())
	}
	if _, err := st.RepoByPath("alice/x"); err == nil {
		t.Fatal("a refused import created a repository")
	}
	if _, err := os.Stat(RepoDir(root, "alice", "x")); !os.IsNotExist(err) {
		t.Fatalf("a refused import left a directory: %v", err)
	}
}

// import.test does not resolve; the import works only because git was
// pinned to the address import looked up and checked.
func TestRepoImportConnectsToTheCheckedAddress(t *testing.T) {
	remote, sha := importUpstream(t)
	u, _ := url.Parse(remote)
	c, errOut, _, root := importCtx(t, true)
	var asked []string
	prev := importLookup
	importLookup = func(ctx context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	t.Cleanup(func() { importLookup = prev })

	code := Dispatch(c, []string{"repo", "import", "alice/copy", "--from", "http://import.test:" + u.Port() + "/remote.git"})
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out := c.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "default main") {
		t.Fatalf("output %q: the default branch was not read through the pin", out)
	}
	got := strings.TrimSpace(gitRunner(t)(RepoDir(root, "alice", "copy"), "rev-parse", "refs/heads/main"))
	if got != sha {
		t.Fatalf("main = %s, want %s", got, sha)
	}
	if !slices.Equal(asked, []string{"import.test"}) {
		t.Fatalf("looked up %v", asked)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run 'TestRepoImport' -count=1`
Expected: build failure, `undefined: importLookup`.

- [ ] **Step 3: Implement the gitutil half**

`internal/gitutil/gitutil.go`, `FetchMirror` with its comment:

```go
// FetchMirror pulls all branches, tags, and notes from a foreign URL into
// the bare repository at dir, forcing updates. Progress streams to errW so
// an interactive caller can watch. pin is git's leading -c options
// (gitpin.Remote.Args); env is git's whole environment and carries
// credentials via GIT_ASKPASS: the URL itself must never contain them.
func FetchMirror(ctx context.Context, dir, url string, errW io.Writer, pin, env []string) error {
	args := append(append([]string{}, pin...), "-C", dir, "fetch", "--progress", "--no-write-fetch-head", url,
		"+refs/heads/*:refs/heads/*",
		"+refs/tags/*:refs/tags/*",
		"+refs/notes/*:refs/notes/*")
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	cmd.Env = env
	cmd.Stderr = errW
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetch from %s: %w", url, err)
	}
	return nil
}
```

`RemoteDefaultBranch`, the comment and the first three lines:

```go
// RemoteDefaultBranch asks the remote which branch HEAD points at. pin
// and env are as for FetchMirror.
func RemoteDefaultBranch(ctx context.Context, url string, pin, env []string) (string, error) {
	args := append(append([]string{}, pin...), "ls-remote", "--symref", url, "HEAD")
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	cmd.Env = env
	out, err := cmd.Output()
```

The rest of `RemoteDefaultBranch` is unchanged.

- [ ] **Step 4: Implement the import half**

Imports in `internal/control/import.go`:

```go
import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitpin"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
)
```

After `askpassScript` (line 39), add:

```go
// importLookup resolves an import's host; tests replace it.
var importLookup gitpin.Lookup = gitpin.LookupIP
```

Replace lines 80-116 (from `// Scheme allowlist.` through the closing
brace of the token `if`/`else`) with:

```go
	// http and https only. git:// has no equivalent of curl's resolve
	// list, so its connection cannot be held to a checked address;
	// file:// would read the server's filesystem, and ssh:// would use
	// the server's own keys.
	if !strings.HasPrefix(from, "https://") && !strings.HasPrefix(from, "http://") {
		return c.fail(protocol.ExitUsage, "import fetches over http:// and https:// only; use the repository's https:// URL")
	}
	if strings.ContainsAny(from, "@") {
		// Credentials belong on stdin, not in the URL where they would
		// land in process listings and logs.
		return c.fail(protocol.ExitUsage, "do not embed credentials in the URL; use --token-stdin")
	}

	// Resolve and check the host now and hold git to those addresses,
	// as mirror sync does (#298).
	timeout := time.Duration(c.Cfg.Limits.CloneTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := gitpin.CheckGit(ctx); err != nil {
		return c.fail(protocol.ExitFailure, "import unavailable: %v", err)
	}
	remote, err := gitpin.Resolve(ctx, importLookup, from, c.Cfg.Webhooks.AllowLocal)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}

	// The token is read from stdin and handed to git via GIT_ASKPASS and
	// the environment — never argv, never the database, never a log line.
	env := gitpin.Env(c.Cfg.Server.Root)
	if tokenStdin {
		token, err := bufio.NewReader(io.LimitReader(c.Stdin, 4096)).ReadString('\n')
		if err != nil && err != io.EOF {
			return c.fail(protocol.ExitFailure, "reading token: %v", err)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return c.fail(protocol.ExitUsage, "--token-stdin given but stdin held no token")
		}
		askpass := filepath.Join(c.Cfg.Server.Root, "askpass.sh")
		if err := os.WriteFile(askpass, []byte(askpassScript), 0o700); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		env = append(env, "GIT_ASKPASS="+askpass, "GITBAY_IMPORT_TOKEN="+token)
	}
```

Replace lines 146-158 (the old `timeout`/`ctx`/`cancel`, the fetch
and the default-branch lookup) with:

```go
	fmt.Fprintf(c.Stderr, "importing %s into %s ...\n", from, path)
	if err := gitutil.FetchMirror(ctx, dir, from, c.Stderr, remote.Args(), env); err != nil {
		cleanup()
		return c.fail(protocol.ExitFailure, "import failed: %v", err)
	}

	branch, err := gitutil.RemoteDefaultBranch(ctx, from, remote.Args(), env)
	if err != nil {
		branch = "main" // remote gone quiet after the fetch; keep the default
	}
```

- [ ] **Step 5: Run the packages**

Run: `go build ./... && go vet ./internal/control ./internal/gitutil && go test ./internal/control ./internal/gitutil -count=1`
Expected: PASS, including the three `TestRepoImport*` tests.

- [ ] **Step 6: Commit**

```bash
git add internal/gitutil/gitutil.go internal/control/import.go internal/control/import_test.go
git commit -S -m "import: http(s) only, address checked and pinned like a mirror sync

git:// is refused: it cannot be held to a checked address.

Ref #298"
```

### Task 2.4: e2e, wiki and changelog

**Files:**
- Modify: `e2e/import_test.go:14`, `:85-92`, `:105-114`
- Modify: `.gitbay/wiki/Threat-Model.org:91-103`, `Architecture/09-Controls.org:78`, `Architecture/10-Known-Gaps.org:17`, `Architecture/02-Components.org` (packages table), `Admin.org:196-198`, `:201`, `Users.org:257-259`
- Modify: `CHANGELOG.org`

**Interfaces:** none.

- [ ] **Step 1: Update the e2e test**

`e2e/import_test.go:14`: the test imports from its own instance on
`127.0.0.1`, which a default instance now refuses:

```go
	inst := startInstanceWith(t, "[webhooks]\nallow_local = true\n")
```

Delete lines 85-92 (`// Import over git:// too.` through the closing
brace of the `git://` import).

The refusal table, lines 105-114:

```go
	// Refusals: bad scheme, git://, credentials in URL, existing name, foreign owner.
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"repo", "import", "alice/x", "--from", "file:///etc"}, "http:// and https:// only"},
		{[]string{"repo", "import", "alice/x", "--from", "git://127.0.0.1:1/alice/src.git"}, "use the repository's https:// URL"},
		{[]string{"repo", "import", "alice/x", "--from", "https://token@github.com/a/b"}, "--token-stdin"},
		{[]string{"repo", "import", "alice/mirror", "--from", httpURL}, "already exists"},
		{[]string{"repo", "import", "bob/x", "--from", httpURL}, "not you and not an organization"},
	}
```

- [ ] **Step 2: Run it**

Run: `go build ./... && go test ./e2e -run 'TestRepoImport$' -count=1`
Expected: PASS.

- [ ] **Step 3: Wiki**

`Threat-Model.org`, the paragraph under `* Network-facing request
forgery` (lines 91-103) becomes:

```org
Webhook delivery, GitHub-history import =--api-base=, mirror remotes
and =repo import --from=, which make the *server* open an outbound
connection to a user-supplied address, pass the same SSRF guard: the
scheme must be http/https and, unless =webhooks.allow_local= is set,
the resolved address must not be loopback, private, shared
(100.64.0.0/10), link-local, or multicast. The webhook dialer re-checks
at connect time, and mirror sync and =repo import= resolve and check
immediately before running git and pin it to the checked addresses
(=internal/gitpin=), so a DNS answer that changes after validation
still cannot reach private space. Redirects are never followed.
=repo import= refuses =git://=, which cannot be pinned.
```

`Architecture/09-Controls.org:78`:

```org
| SSRF protection on user-supplied URLs       | in place | webhooks at save and connect; mirrors at save and sync, =repo import= before its fetch, git pinned to the checked address (=internal/gitpin=) |
```

`Architecture/10-Known-Gaps.org`: delete the `#298` row (line 17).

`Architecture/02-Components.org`, after the `=internal/mirror=` row:

```org
| =internal/gitpin=    | Resolves and checks a user-supplied http(s) remote and pins git to the checked addresses; mirror sync and =repo import=. |
```

`Admin.org:196-198` (`[webhooks]`):

```org
- =allow_local= (false) — permit webhook, mirror and import targets on
  loopback, private, shared (100.64.0.0/10), link-local or multicast
  addresses. Leave off unless you know why you need it (SSRF).
```

`Admin.org:201` (`[limits]`):

```org
- =clone_timeout= (3600s) — cap on =repo import= fetches. An import
  takes http and https URLs only, passes the same address check as a
  mirror sync and is pinned the same way, so it needs git 2.37 too.
```

`Users.org`, after the `#+end_src` at line 257 and before
`Sourcehut has no API of that shape`:

```org
=repo import= fetches over http and https only, from an address that
passes the same check as a webhook target; a =git://= URL is refused,
so use the repository's https URL.

```

- [ ] **Step 4: Changelog**

Append to the `* Unreleased` list, directly above `* v1.36.0 — 2026-09-23`:

```org
- =repo import --from= takes http and https URLs only; =git://= is
  refused, since its connection cannot be held to a checked address.
  The host is resolved and checked like a mirror's, git connects only
  to the checked addresses with redirects off, and the system and
  global gitconfig are ignored. A source that redirects (a renamed
  repository) fails; import from the URL it redirects to. Needs git
  2.37 or later on the server (#298).
```

- [ ] **Step 5: Commit, MR, merge**

```bash
git add e2e/import_test.go .gitbay/wiki/Threat-Model.org .gitbay/wiki/Architecture/09-Controls.org \
  .gitbay/wiki/Architecture/10-Known-Gaps.org .gitbay/wiki/Architecture/02-Components.org \
  .gitbay/wiki/Admin.org .gitbay/wiki/Users.org CHANGELOG.org
git commit -S -m "import: document the address check; e2e imports with allow_local

Closes #298"
git push -u origin import-pin-address
gitbay mr create --source import-pin-address --target main --title "repo import: http(s) only, address checked and pinned"
```

Merge `--strategy ff` after CI, delete the branch both places.

---

# MR 3: LFS tokens die with their key (branch `lfs-token-key`, closes #285)

### Task 3.1: the token names the key that obtained it

**Files:**
- Modify: `internal/lfs/lfs.go:122-169` (`Sign`, `Verify`)
- Create: `internal/lfs/lfs_test.go`
- Modify: `internal/sshd/lfs.go:16-78` (`runLFSAuthenticate`)
- Modify: `internal/sshd/sshd.go:499` (call site)
- Modify: `internal/httpd/lfs.go:38-58` (`lfsAuth`), `:99`, `:130` (`lfsBatch`), `:180-185` (`lfsDownload`), `:199-204` (`lfsUpload`)

**Interfaces:**
- Produces:
  - `func Sign(secret []byte, repoID, keyID int64, op string, now time.Time) string`
  - `type Grant struct { RepoID, KeyID int64; Op string }`
  - `func Verify(secret []byte, token string, now time.Time) (Grant, bool)`
  - `func runLFSAuthenticate(cfg config.Config, st *store.Store, user store.User, key store.SSHKey, argv []string, stdout, stderr io.Writer) int`
  - `func (s *Server) lfsAuth(r *http.Request, repo store.Repo) (op string, keyID int64)`
- Consumed by Task 3.2: `lfsAuth`'s signature, `Grant.KeyID`.

- [ ] **Step 1: Write the failing tests**

`internal/lfs/lfs_test.go`:

```go
package lfs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestTokenCarriesTheKey(t *testing.T) {
	secret := []byte("secret")
	now := time.Now()
	tok := Sign(secret, 7, 42, "upload", now)
	g, ok := Verify(secret, tok, now)
	if !ok || g != (Grant{RepoID: 7, KeyID: 42, Op: "upload"}) {
		t.Fatalf("Verify = %+v, %v", g, ok)
	}
	if _, ok := Verify(secret, tok, now.Add(TokenTTL+time.Second)); ok {
		t.Error("an expired token verified")
	}
	if _, ok := Verify([]byte("other"), tok, now); ok {
		t.Error("a token verified under another secret")
	}
	if g, ok := Verify(secret, Sign(secret, 7, 0, "download", now), now); !ok || g.KeyID != 0 {
		t.Errorf("anonymous grant = %+v, %v", g, ok)
	}
}

// A token minted before tokens named their key has three fields. It is
// refused, not read as a grant bound to no key (#285).
func TestUnboundTokenRefused(t *testing.T) {
	secret := []byte("secret")
	payload := fmt.Sprintf("%d:%s:%d", 7, "upload", time.Now().Add(TokenTTL).Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	tok := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if g, ok := Verify(secret, tok, time.Now()); ok {
		t.Fatalf("a pre-upgrade token verified: %+v", g)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/lfs -count=1`
Expected: build failure, `too many arguments in call to Sign` /
`undefined: Grant`.

- [ ] **Step 3: Implement the lfs package**

Replace lines 122-169 of `internal/lfs/lfs.go`:

```go
// Tokens bridge SSH authentication to the HTTP endpoints: stateless,
// HMAC-signed, scoped to one repo and one operation, short-lived, and
// bound to the SSH key that obtained them, which must still be live
// when the token is used (#285). The secret persists in the settings
// table so tokens survive restarts.

const TokenTTL = time.Hour

// Sign mints a token for op ("download" or "upload") on repoID, bound
// to keyID: the SSH key, user or deploy, that asked for it, or 0 for an
// anonymous download of a public repository.
func Sign(secret []byte, repoID, keyID int64, op string, now time.Time) string {
	payload := fmt.Sprintf("%d:%d:%s:%d", repoID, keyID, op, now.Add(TokenTTL).Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Grant is what a verified token authorizes.
type Grant struct {
	RepoID int64
	KeyID  int64 // 0: an anonymous download of a public repository
	Op     string
}

// Verify checks a token's MAC, shape and expiry. A token from before
// tokens named their key does not verify.
func Verify(secret []byte, token string, now time.Time) (Grant, bool) {
	payloadB64, macB64, found := strings.Cut(token, ".")
	if !found {
		return Grant{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Grant{}, false
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return Grant{}, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), gotMAC) {
		return Grant{}, false
	}
	parts := strings.Split(string(payload), ":")
	if len(parts) != 4 {
		return Grant{}, false
	}
	repoID, err1 := strconv.ParseInt(parts[0], 10, 64)
	keyID, err2 := strconv.ParseInt(parts[1], 10, 64)
	exp, err3 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || keyID < 0 || now.Unix() > exp {
		return Grant{}, false
	}
	if parts[2] != "download" && parts[2] != "upload" {
		return Grant{}, false
	}
	return Grant{RepoID: repoID, KeyID: keyID, Op: parts[2]}, true
}
```

- [ ] **Step 4: Update the callers**

`internal/sshd/lfs.go`, lines 16-29 (doc comment, signature and the
usage check) become:

```go
// runLFSAuthenticate answers the git-lfs client's SSH probe:
//
//	git-lfs-authenticate <path> download|upload
//
// with the HTTP endpoint and a short-lived repo- and operation-scoped
// token. Access rules mirror the git transports: download needs read,
// upload needs write; deploy keys authorize by their binding alone, and
// every denial on an invisible repo reads as nonexistence. The token
// names key, so it stops working when the key does (#285).
func runLFSAuthenticate(cfg config.Config, st *store.Store, user store.User, key store.SSHKey,
	argv []string, stdout, stderr io.Writer) int {
	scope := key.Scope
	if len(argv) != 3 || (argv[2] != "download" && argv[2] != "upload") {
		fmt.Fprintln(stderr, "usage: git-lfs-authenticate <path> download|upload")
		return protocol.ExitUsage
	}
```

Lines 30-69 stay as they are (they read `scope`). Line 70 becomes:

```go
	token := lfs.Sign([]byte(secret), repo.ID, key.ID, op, time.Now())
```

`internal/sshd/sshd.go:499`:

```go
			return runLFSAuthenticate(cfg, st, user, key, argv, stdout, stderr)
```

`internal/httpd/lfs.go`, `lfsAuth` for now carries the key id through
without checking it (Task 3.2 adds the check):

```go
// lfsAuth resolves what the request may do to the repo: "upload",
// "download", or "" for no access, and the key the grant rests on (0
// for none). Tokens are repo-scoped; without one, public repos allow
// anonymous download only.
func (s *Server) lfsAuth(r *http.Request, repo store.Repo) (string, int64) {
	auth := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
		secret, err := s.lfsSecret()
		if err != nil {
			return "", 0
		}
		g, ok := lfs.Verify(secret, tok, time.Now())
		if !ok || g.RepoID != repo.ID {
			return "", 0
		}
		return g.Op, g.KeyID
	}
	if repo.Visibility == "public" {
		return "download", 0
	}
	return "", 0
}
```

`lfsBatch` line 99 and line 130:

```go
	granted, keyID := s.lfsAuth(r, repo)
```

```go
	transferToken := lfs.Sign(secret, repo.ID, keyID, req.Operation, time.Now())
```

`lfsDownload`, lines 181-185:

```go
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
	if op, _ := s.lfsAuth(r, repo); op == "" {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
```

`lfsUpload`, lines 200-204:

```go
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
	if op, _ := s.lfsAuth(r, repo); op != "upload" {
		lfsError(w, http.StatusNotFound, "not found")
		return
	}
```

- [ ] **Step 5: Run the packages**

Run: `go build ./... && go vet ./internal/lfs ./internal/sshd ./internal/httpd && go test ./internal/lfs ./internal/sshd ./internal/httpd -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/lfs internal/sshd/lfs.go internal/sshd/sshd.go internal/httpd/lfs.go
git commit -S -m "lfs: a transfer token names the key that obtained it

A pre-upgrade token, which names none, no longer verifies.

Ref #285"
```

### Task 3.2: an LFS request checks the token's key is live

**Files:**
- Modify: `internal/httpd/lfs.go` (`lfsAuth`)
- Create: `internal/httpd/lfsauth_test.go`

**Interfaces:**
- Consumes: `lfs.Sign`, `lfs.Verify`, `Grant` (Task 3.1); `store.LiveSSHKeys(ids []int64) (map[int64]bool, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/httpd/lfsauth_test.go`:

```go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/lfs"
	"gitbay.org/gitbay/internal/store"
)

func lfsRequest(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/alice/app.git/info/lfs/objects/x", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

func lfsTestRepo(t *testing.T, st *store.Store, uid int64, name, visibility string) store.Repo {
	t.Helper()
	id, err := st.CreateRepo("user", uid, name, visibility)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// A token works only while its key does: removed, expired or on a
// disabled account, the key takes its tokens with it (#285).
func TestLFSTokenNeedsALiveKey(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	repo := lfsTestRepo(t, st, u.ID, "app", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	addKey := func(fp string, exp *time.Time) int64 {
		t.Helper()
		if err := st.AddSSHKeyFrom(u.ID, fp, "ssh-ed25519", []byte(fp), "full", "", store.KeyOrigin{ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
		k, err := st.SSHKeyByFingerprint(fp)
		if err != nil {
			t.Fatal(err)
		}
		return k.ID
	}

	live := addKey("SHA256:live", nil)
	tok := lfs.Sign(secret, repo.ID, live, "upload", time.Now())
	if op, key := s.lfsAuth(lfsRequest(tok), repo); op != "upload" || key != live {
		t.Fatalf("live key: %q, %d", op, key)
	}

	past := time.Now().Add(-time.Minute)
	expired := addKey("SHA256:expired", &past)
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, repo.ID, expired, "upload", time.Now())), repo); op != "" {
		t.Errorf("expired key: %q", op)
	}

	if err := st.RemoveSSHKey(u.ID, "SHA256:live"); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(tok), repo); op != "" {
		t.Errorf("removed key: %q", op)
	}

	other := addKey("SHA256:other", nil)
	otherTok := lfs.Sign(secret, repo.ID, other, "download", time.Now())
	if op, _ := s.lfsAuth(lfsRequest(otherTok), repo); op != "download" {
		t.Fatalf("second key before disable: %q", op)
	}
	if err := st.SetUserDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if op, _ := s.lfsAuth(lfsRequest(otherTok), repo); op != "" {
		t.Errorf("disabled account: %q", op)
	}
}

// A token with no key comes from an anonymous batch on a public
// repository and is worth exactly what anonymous is: a download, while
// the repository is public.
func TestLFSAnonymousTokenOnlyDownloadsPublic(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	pub := lfsTestRepo(t, st, u.ID, "big", "public")
	priv := lfsTestRepo(t, st, u.ID, "vault", "private")
	secret, err := s.lfsSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, pub.ID, 0, "download", now)), pub); op != "download" {
		t.Errorf("public download: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, pub.ID, 0, "upload", now)), pub); op != "" {
		t.Errorf("anonymous upload: %q", op)
	}
	if op, _ := s.lfsAuth(lfsRequest(lfs.Sign(secret, priv.ID, 0, "download", now)), priv); op != "" {
		t.Errorf("private download: %q", op)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/httpd -run 'TestLFS' -count=1`
Expected: FAIL: `expired key: "upload"`, `removed key: "upload"`,
`disabled account: "download"`, `anonymous upload: "upload"`,
`private download: "download"`.

- [ ] **Step 3: Implement**

`lfsAuth`:

```go
// lfsAuth resolves what the request may do to the repo: "upload",
// "download", or "" for no access, and the key the grant rests on (0
// for none). A token is bound to the SSH key that obtained it and
// works only while that key is registered, unexpired and on an enabled
// account (#285). Without one, public repos allow anonymous download
// only.
func (s *Server) lfsAuth(r *http.Request, repo store.Repo) (string, int64) {
	auth := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
		secret, err := s.lfsSecret()
		if err != nil {
			return "", 0
		}
		g, ok := lfs.Verify(secret, tok, time.Now())
		if !ok || g.RepoID != repo.ID {
			return "", 0
		}
		if g.KeyID == 0 {
			// Minted by an anonymous batch: worth what anonymous is.
			if g.Op == "download" && repo.Visibility == "public" {
				return "download", 0
			}
			return "", 0
		}
		live, err := s.st.LiveSSHKeys([]int64{g.KeyID})
		if err != nil || !live[g.KeyID] {
			return "", 0
		}
		return g.Op, g.KeyID
	}
	if repo.Visibility == "public" {
		return "download", 0
	}
	return "", 0
}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/httpd -count=1 && go vet ./internal/httpd`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpd/lfs.go internal/httpd/lfsauth_test.go
git commit -S -m "lfs: refuse a token whose key was removed, expired or disabled

Ref #285"
```

### Task 3.3: e2e, wiki and changelog

**Files:**
- Modify: `e2e/lfs_test.go` (new test after `TestLFS`)
- Modify: `.gitbay/wiki/Architecture/05-Identity-and-Access.org:25`, `Architecture/04-Trust-Boundaries.org:109-116`, `Architecture/09-Controls.org:28`, `Threat-Model.org:44-53`
- Modify: `CHANGELOG.org`

**Interfaces:**
- Consumes: e2e helpers `startInstance`, `inst.newKey`, `inst.admin`, `inst.ssh`, `fingerprint` (`e2e/revoke_test.go:38`).

- [ ] **Step 1: Write the e2e test**

Append to `e2e/lfs_test.go`:

```go
// A transfer token works only while the key that obtained it does:
// removing the key ends it before its hour is up (#285). No git-lfs
// client needed: the token comes from git-lfs-authenticate over SSH.
func TestLFSTokenEndsWithItsKey(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/vault", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	spare := inst.newKey(t, "spare")
	pub, err := os.ReadFile(spare + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "keys", "add"); code != 0 {
		t.Fatalf("keys add: %s", errOut)
	}
	out, errOut, code := inst.ssh(t, spare, "", "git-lfs-authenticate", "alice/vault", "download")
	if code != 0 {
		t.Fatalf("authenticate: %s", errOut)
	}
	var grant struct {
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal([]byte(out), &grant); err != nil {
		t.Fatalf("authenticate JSON: %v\n%s", err, out)
	}
	batch := func() int {
		body := fmt.Sprintf(`{"operation":"download","transfers":["basic"],"objects":[{"oid":%q,"size":4}]}`, strings.Repeat("ab", 32))
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("http://127.0.0.1:%d/alice/vault.git/info/lfs/objects/batch", inst.httpPort),
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		req.Header.Set("Authorization", grant.Header["Authorization"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := batch(); code != 200 {
		t.Fatalf("batch with a live key: %d", code)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "keys", "remove", fingerprint(t, spare+".pub")); code != 0 {
		t.Fatalf("keys remove: %s", errOut)
	}
	if code := batch(); code != 404 {
		t.Fatalf("batch after the key was removed: %d, want 404", code)
	}
}
```

The file already imports `encoding/json`, `fmt`, `net/http`, `os` and
`strings`.

- [ ] **Step 2: Run it**

Run: `go build ./... && go test ./e2e -run 'TestLFSTokenEndsWithItsKey$' -count=1`
Expected: PASS.

- [ ] **Step 3: Wiki**

`Architecture/05-Identity-and-Access.org:25`, the LFS row:

```org
| LFS transfer token | HMAC-SHA256 over repo, SSH key, operation, expiry | not stored (stateless)       | one repository, upload or download         | 1 h                           | with its key: refused once the key is removed or expires, or its account is disabled |
```

`Architecture/04-Trust-Boundaries.org`, section `** F. LFS`, the
paragraph becomes:

```org
=git-lfs-authenticate= over SSH applies the same repository checks as
git transport and returns a one-hour HMAC token scoped to repository,
operation and the SSH key that asked for it, deploy keys included
(=internal/sshd/lfs.go=, =internal/lfs/lfs.go=). The HTTP batch, upload
and download endpoints verify that token and that its key is still
registered, unexpired and on an enabled account (=store.LiveSSHKeys=);
public repositories allow anonymous download. Objects are verified
against their SHA-256 id on upload.
```

`Architecture/09-Controls.org:28`:

```org
| Revocation takes effect immediately         | in place | removing a key or disabling an account closes its connections; every exec re-reads its key (=internal/sshd/sshd.go=); LFS transfer tokens are refused with their key (=internal/httpd/lfs.go=) |
```

`Threat-Model.org`, the `*Revocation is immediate.*` bullet: after
"a push killed before its pre-receive hook answers moves no ref."
insert:

```org
  An LFS transfer token names the key that obtained it and is refused
  from the moment that key is.
```

- [ ] **Step 4: Changelog**

Append to the `* Unreleased` list, directly above `* v1.36.0 — 2026-09-23`:

```org
- LFS transfer tokens name the SSH key that obtained them, deploy keys
  included, and every LFS request checks that the key is still
  registered, unexpired and on an enabled account: removing a key or
  disabling an account ends its tokens at once instead of within the
  hour (#285). *Operators:* tokens minted before the upgrade are
  refused. git-lfs asks =git-lfs-authenticate= for a token each time
  it runs, so only a transfer running across the restart fails, with
  "repository not found"; running the command again fixes it.
```

- [ ] **Step 5: Commit, MR, merge**

```bash
git add e2e/lfs_test.go .gitbay/wiki/Architecture/05-Identity-and-Access.org \
  .gitbay/wiki/Architecture/04-Trust-Boundaries.org .gitbay/wiki/Architecture/09-Controls.org \
  .gitbay/wiki/Threat-Model.org CHANGELOG.org
git commit -S -m "lfs: e2e for a token outliving its key; document the binding

Closes #285"
git push -u origin lfs-token-key
gitbay mr create --source lfs-token-key --target main --title "lfs: transfer tokens die with the key that obtained them"
```

Merge `--strategy ff` after CI, delete the branch both places.

---

# MR 4: minting from the web needs a recent sign-in (branch `web-mint-reauth`, closes #297)

### Task 4.1: the session's sign-in time reaches the user

**Files:**
- Modify: `internal/store/users.go:11-17` (`User`)
- Modify: `internal/store/sessions.go:83-102` (`WebSessionUser`)
- Test: `internal/store/sessions_test.go`

**Interfaces:**
- Produces: `store.User.SignedInAt time.Time` — set by `WebSessionUser` from `web_sessions.created_at`; zero on every other path.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sessions_test.go`:

```go
// A session's sign-in time is its creation; using the session renews
// its idle expiry and leaves the sign-in time alone (#297).
func TestWebSessionUserSignedInAt(t *testing.T) {
	s, uid := sessionFixture(t)
	_, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWebSession(hash, uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	u, err := s.WebSessionUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(u.SignedInAt); age < 0 || age > time.Minute {
		t.Fatalf("fresh session signed in %v ago", age)
	}

	signedIn := time.Now().Add(-2 * time.Hour)
	if _, err := s.DB.Exec("UPDATE web_sessions SET created_at = ?, last_used_at = ? WHERE token_hash = ?",
		fmtTime(signedIn), fmtTime(signedIn), hash); err != nil {
		t.Fatal(err)
	}
	u, err = s.WebSessionUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	if err := s.DB.QueryRow("SELECT last_used_at FROM web_sessions WHERE token_hash = ?", hash).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last == fmtTime(signedIn) {
		t.Fatal("using the session did not renew it")
	}
	if want := signedIn.UTC().Truncate(time.Millisecond); !u.SignedInAt.Equal(want) {
		t.Fatalf("SignedInAt = %v, want %v", u.SignedInAt, want)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run TestWebSessionUserSignedInAt -count=1`
Expected: build failure, `u.SignedInAt undefined`.

- [ ] **Step 3: Implement**

`internal/store/users.go`, `User`:

```go
type User struct {
	ID       int64
	Username string
	IsAdmin  bool
	Pending  bool // self-registered, email not yet verified
	Disabled bool // administratively suspended
	// SignedInAt is when the browser session this user came from was
	// created by a login. Set by WebSessionUser only; zero elsewhere.
	SignedInAt time.Time
}
```

`internal/store/sessions.go`, `WebSessionUser`:

```go
// WebSessionUser resolves a session cookie hash to its user, with the
// session's sign-in time, and renews the session's idle expiry. A
// session is written at most once a minute, so a burst of requests
// costs one UPDATE. Renewal never moves created_at: only a login
// creates a session, so created_at is when it signed in.
func (s *Store) WebSessionUser(hash string) (User, error) {
	now := time.Now()
	var userID int64
	var created string
	err := s.DB.QueryRow(
		"SELECT user_id, created_at FROM web_sessions WHERE token_hash = ? AND expires_at > ?",
		hash, fmtTime(now)).Scan(&userID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	s.DB.Exec(`UPDATE web_sessions SET last_used_at = ?, expires_at = min(absolute_expires_at, ?)
		WHERE token_hash = ? AND last_used_at < ?`,
		fmtTime(now), fmtTime(now.Add(WebSessionIdle)), hash, fmtTime(now.Add(-time.Minute)))
	u, err := s.UserByID(userID)
	if err != nil {
		return User{}, err
	}
	if t := parseTime(sql.NullString{String: created, Valid: true}); t != nil {
		u.SignedInAt = *t
	}
	return u, nil
}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/store -count=1 && go vet ./internal/store`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/users.go internal/store/sessions.go internal/store/sessions_test.go
git commit -S -m "store: a web session's user carries its sign-in time

Ref #297"
```

### Task 4.2: Dispatch refuses a stale session's credential mint

**Files:**
- Modify: `internal/control/control.go:21-70` (`Ctx`), `:205-209` (`runChecked`, the `Expires` block)
- Create: `internal/control/reauth_test.go`

**Interfaces:**
- Consumes: none from Task 4.1 (the field is filled by httpd in Task 4.3).
- Produces:
  - `const ReauthWindow = 15 * time.Minute`
  - `const ReauthRefusal = "creating a credential from the web needs a sign-in from the last 15 minutes; sign in again, then submit the form again"`
  - `Ctx.SignedInAt *time.Time`

- [ ] **Step 1: Write the failing test**

`internal/control/reauth_test.go`:

```go
package control

import (
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A browser session mints credentials only within ReauthWindow of
// signing in; SSH and the API carry no session and are not affected
// (#297).
func TestMintNeedsRecentWebSignIn(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	user := store.User{ID: uid, Username: "alice"}
	run := func(signedIn *time.Time, stdin string, argv ...string) (string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), user)
		c.Cfg.Limits.WriteRate = -1
		c.SignedInAt = signedIn
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return strings.TrimSpace(errOut.String()), code
	}
	fresh := time.Now().Add(-time.Minute)
	stale := time.Now().Add(-ReauthWindow - time.Minute)

	if msg, code := run(&stale, authorizedKey(t, "stale"), "keys", "add"); code != protocol.ExitDenied || msg != ReauthRefusal {
		t.Fatalf("stale session: exit %d, %q", code, msg)
	}
	if msg, code := run(&fresh, authorizedKey(t, "fresh"), "keys", "add"); code != protocol.ExitOK {
		t.Fatalf("fresh session: exit %d, %q", code, msg)
	}
	// SSH and the API set no SignedInAt.
	if msg, code := run(nil, authorizedKey(t, "ssh"), "keys", "add"); code != protocol.ExitOK {
		t.Fatalf("no session: exit %d, %q", code, msg)
	}
	// A command that mints nothing is not held back.
	if msg, code := run(&stale, "", "keys", "list"); code != protocol.ExitOK {
		t.Fatalf("keys list on a stale session: exit %d, %q", code, msg)
	}
	keys, err := st.ListSSHKeys(uid)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %d %v, want the fresh and the ssh one", len(keys), err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestMintNeedsRecentWebSignIn -count=1`
Expected: build failure, `c.SignedInAt undefined`, `undefined: ReauthWindow`.

- [ ] **Step 3: Implement**

`Ctx`, after `Expires`:

```go
	// SignedInAt is when the browser session behind this request signed
	// in; nil off the web. Dispatch refuses MintsCredential commands when
	// it is older than ReauthWindow.
	SignedInAt *time.Time
```

After the `Ctx` type:

```go
// ReauthWindow is how long after signing in a browser session may run a
// MintsCredential command. A session lasts days and its cookie is a
// bearer credential; what it creates must come from a recent sign-in
// (#297).
const ReauthWindow = 15 * time.Minute

// ReauthRefusal is what a web session signed in longer ago than
// ReauthWindow gets; the web shows a sign-in link beside it.
const ReauthRefusal = "creating a credential from the web needs a sign-in from the last 15 minutes; sign in again, then submit the form again"
```

`runChecked`, directly after the `Expires` block:

```go
	if cmd.MintsCredential && c.SignedInAt != nil && time.Since(*c.SignedInAt) > ReauthWindow {
		return c.fail(protocol.ExitDenied, "%s", ReauthRefusal)
	}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/control -count=1 && go vet ./internal/control`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/control.go internal/control/reauth_test.go
git commit -S -m "control: a web session mints credentials only soon after signing in

Ref #297"
```

### Task 4.3: every web dispatch carries the sign-in time; the refusal links to sign-in

**Files:**
- Modify: `internal/httpd/control.go:25-200` (the five `control.Ctx` literals → `webCtx`)
- Modify: `internal/httpd/flash.go` (add `reauthNotice`; import `control`)
- Modify: `internal/httpd/account.go:131-154` (`renderAccount`)
- Modify: `internal/httpd/settings.go:20-30` (`settingsPage`), `:67-74` (`settingsFormWith`)
- Modify: `internal/web/templates/account.html:5`, `settings.html:6`
- Modify: `internal/httpd/account_test.go:289-303` (`newTokenTestServer`)
- Create: `internal/httpd/reauth_test.go`

**Interfaces:**
- Consumes: `store.User.SignedInAt` (Task 4.1); `control.Ctx.SignedInAt`, `control.ReauthWindow`, `control.ReauthRefusal` (Task 4.2).
- Produces:
  - `func (s *Server) webCtx(u store.User, stdin string, stdout, stderr io.Writer) *control.Ctx`
  - `func (s *Server) reauthNotice(w http.ResponseWriter, notice, path string) bool`
  - page fields `Reauth bool` on the account page struct and `settingsPage`.

- [ ] **Step 1: Write the failing tests**

`internal/httpd/reauth_test.go`:

```go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// A session signed in longer ago than ReauthWindow cannot mint from the
// settings page: the form comes back with the refusal and a sign-in
// link, and the sign-in returns to /settings (#297).
func TestWebMintNeedsRecentSignIn(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	stale := u
	stale.SignedInAt = time.Now().Add(-control.ReauthWindow - time.Minute)
	rr := submitAccountForm(t, s, stale, url.Values{"field": {"token-create"}, "name": {"laptop"}, "scope": {"full"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if list, err := st.ListAPITokens(u.ID); err != nil || len(list) != 0 {
		t.Fatalf("a stale session minted %+v (%v)", list, err)
	}

	req := httptest.NewRequest("GET", "/settings", nil)
	for _, c := range rr.Result().Cookies() {
		req.AddCookie(c)
	}
	page := httptest.NewRecorder()
	s.accountPage(page, req, stale)
	body := page.Body.String()
	if !strings.Contains(body, control.ReauthRefusal) {
		t.Fatalf("refusal not shown: %s", body)
	}
	if !strings.Contains(body, `<a href="/login">Sign in again</a>`) {
		t.Fatalf("no sign-in link: %s", body)
	}
	var next string
	for _, c := range page.Result().Cookies() {
		if c.Name == nextCookie {
			next = c.Value
		}
	}
	if next != url.QueryEscape("/settings") {
		t.Fatalf("gitbay_next = %q, want /settings", next)
	}
}

// An API token has no browser session: minting through the API is not
// held to the sign-in window.
func TestAPIMintIgnoresTheSignInWindow(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	if err := st.CreateAPIToken(u.ID, "ci", store.HashToken("gb_reauthtest"), "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/cmd",
		strings.NewReader(`{"argv":["token","create","--name","second","--scope","read"]}`))
	req.Header.Set("Authorization", "Bearer gb_reauthtest")
	rr := httptest.NewRecorder()
	s.apiCmd(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
}
```

`internal/httpd/account_test.go`, `newTokenTestServer`'s return: the
session behind the tests' forms signed in just now, so the minting
tests exercise a fresh session:

```go
	return New(config.Default(), st, nil), st, store.User{ID: uid, Username: "alice", SignedInAt: time.Now()}
```

(`account_test.go` gains the `"time"` import if it lacks it.)

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/httpd -run 'TestWebMintNeedsRecentSignIn|TestAPIMintIgnoresTheSignInWindow' -count=1`
Expected: `TestWebMintNeedsRecentSignIn` FAILs with `status 200`
(the stale session minted); `TestAPIMintIgnoresTheSignInWindow` PASSes.

- [ ] **Step 3: One Ctx for every web dispatch**

`internal/httpd/control.go`, after `runControl`:

```go
// webCtx is the Ctx every web dispatch runs under: the session's user
// with full scope, and when that session signed in, which Dispatch
// checks before a command that mints a credential (#297).
func (s *Server) webCtx(u store.User, stdin string, stdout, stderr io.Writer) *control.Ctx {
	signedIn := u.SignedInAt
	return &control.Ctx{
		User:       u,
		Source:     "web",
		Scope:      "full",
		Store:      s.st,
		Cfg:        s.cfg,
		Stdin:      strings.NewReader(stdin),
		Stdout:     stdout,
		Stderr:     stderr,
		ViaAPI:     true,
		SignedInAt: &signedIn,
	}
}
```

Replace the five literals:

- `runControlCode`: `ctx := s.webCtx(u, "", &stdout, &stderr)`
- `runControlStream`:
  ```go
  	ctx := s.webCtx(u, "", out, &stderr)
  	ctx.Done = done
  	ctx.Stopping = s.stopping
  ```
- `runControlStdinCode`: `ctx := s.webCtx(u, stdin, &stdout, &stderr)`
- `dispatchIntoStdin`:
  ```go
  	ctx := s.webCtx(u, stdin, &stdout, &stderr)
  	ctx.JSON = true
  ```
- `dispatchJSON`:
  ```go
  	ctx := s.webCtx(u, stdin, &stdout, &stderr)
  	ctx.JSON = true
  ```

`internal/httpd/flash.go`, import `"gitbay.org/gitbay/internal/control"`
and add after `peekNext`:

```go
// reauthNotice reports whether notice is Dispatch's refusal for a
// session that signed in too long ago to mint a credential and, when it
// is, remembers path so the sign-in the page links to returns there
// (#297).
func (s *Server) reauthNotice(w http.ResponseWriter, notice, path string) bool {
	if notice != control.ReauthRefusal {
		return false
	}
	s.setNext(w, path)
	return true
}
```

- [ ] **Step 4: The pages show the link**

`internal/httpd/account.go`, `renderAccount`: before `s.render(...)`:

```go
	notice := s.takeFlash(w, r)
	reauth := s.reauthNotice(w, notice, "/settings")
```

In the page struct, after `TokenShown`:

```go
		Reauth       bool   // Notice is the stale-session refusal: link to sign in
```

and the value list:

```go
	}{s.baseFor(u), "account", keys, pgp, emails, profile, profileLinksText(profile.Links),
		aboutRepo, aboutEdit, s.cfg.SiteHost(),
		notice, r.URL.Query().Get("m"), mailOn, watchOn, pushOn, devices, theme,
		tokens, tokenShown, reauth})
```

`internal/web/templates/account.html:5`:

```html
{{if .Notice}}<p class="error" role="alert">{{.Notice}}{{if .Reauth}} <a href="/login">Sign in again</a>{{end}}</p>{{end}}
```

`internal/httpd/settings.go`, `settingsPage` gains after `Saved`:

```go
	Reauth      bool // Notice is the stale-session refusal: link to sign in
```

and `settingsFormWith`'s render:

```go
	s.render(w, "settings.html", settingsPage{
		repoPage: p, Topics: topics, Branches: branches,
		DepsEnabled: deps.Enabled, Deps: deps,
		Runners:   runners,
		Notice:    notice,
		Saved:     strings.HasPrefix(notice, "Saved "),
		Reauth:    s.reauthNotice(w, notice, r.URL.Path),
		Submitted: subm,
	})
```

`internal/web/templates/settings.html:6`:

```html
{{if .Notice}}{{if .Saved}}<p class="notice" role="status">{{.Notice}}</p>{{else}}<p class="error" role="alert">{{.Notice}}{{if .Reauth}} <a href="/login">Sign in again</a>{{end}}</p>{{end}}{{end}}
```

- [ ] **Step 5: Run the packages**

Run: `go build ./... && go vet ./internal/httpd ./internal/web && go test ./internal/httpd ./internal/web -count=1`
Expected: PASS, including the existing token tests on the fresh
session from `newTokenTestServer`.

Run: `go test ./e2e -run 'TestAccountSettingsWeb$' -count=1`
Expected: PASS (its session is created by a login moments earlier).

- [ ] **Step 6: Commit**

```bash
git add internal/httpd internal/web/templates/account.html internal/web/templates/settings.html
git commit -S -m "web: dispatch carries the session's sign-in time; a stale one gets a sign-in link

Ref #297"
```

### Task 4.4: wiki and changelog

**Files:**
- Modify: `.gitbay/wiki/Threat-Model.org:57-63`, `Architecture/09-Controls.org:29`, `Architecture/10-Known-Gaps.org:16`, `Architecture/05-Identity-and-Access.org:21`, `:106-120`
- Modify: `CHANGELOG.org`

**Interfaces:** none.

- [ ] **Step 1: Wiki**

`Threat-Model.org`, in the `*The control plane is one command
registry*` bullet, replace `Browser sessions are not covered yet
(#297).` with:

```org
  A browser session can create one only within 15 minutes of signing
  in (=control.ReauthWindow=, #297).
```

`Architecture/09-Controls.org:29`:

```org
| Delegation bounded by the delegating credential | in place | expiring tokens refused on =MintsCredential= commands; credentials record their creating token; a browser session mints only within 15 minutes of signing in (=internal/control/control.go=) |
```

`Architecture/10-Known-Gaps.org`: delete the `#297` row (line 16).

`Architecture/05-Identity-and-Access.org:21`, the web session row:

```org
| Web session        | 32 random bytes hex, cookie =gitbay_session=    | SHA-256 hash                     | full account; credential-minting forms only within 15 minutes of sign-in | 12 h idle, 7 days absolute    | logout, =web sessions revoke=       |
```

and in `* Session security on the web`, after the destructive-actions
bullet:

```org
- Forms that create a credential (SSH and runner keys, API tokens,
  email verification) need a sign-in from the last 15 minutes; an
  older session gets the form back with a sign-in link that returns to
  it (=control.ReauthWindow=, =internal/httpd/flash.go=).
```

- [ ] **Step 2: Changelog**

Append to the `* Unreleased` list, directly above `* v1.36.0 — 2026-09-23`:

```org
- A browser session creates credentials (SSH and runner keys, API
  tokens, verified addresses) only within 15 minutes of signing in. An
  older session gets the form back with a "Sign in again" link, and the
  login link returns to the form. SSH and API tokens are unaffected
  (#297).
```

- [ ] **Step 3: Commit, MR, merge**

```bash
git add .gitbay/wiki/Threat-Model.org .gitbay/wiki/Architecture/09-Controls.org \
  .gitbay/wiki/Architecture/10-Known-Gaps.org .gitbay/wiki/Architecture/05-Identity-and-Access.org \
  CHANGELOG.org
git commit -S -m "wiki: web credential minting needs a recent sign-in

Closes #297"
git push -u origin web-mint-reauth
gitbay mr create --source web-mint-reauth --target main --title "web: minting a credential needs a sign-in from the last 15 minutes"
```

Merge `--strategy ff` after CI, delete the branch both places.

---

## Out of scope, noted

- `repo import-issues` validates `--api-base` once
  (`internal/control/ghimport.go:162`) and then fetches pull heads with
  `gitutil.FetchPullHeads` (`ghimport.go:364-379`) and calls the API
  with a plain `http.Client`, neither pinned. It is the same class of
  gap as #298 and is not in any of the five issues; filed as #301
  rather than widening MR 2.
