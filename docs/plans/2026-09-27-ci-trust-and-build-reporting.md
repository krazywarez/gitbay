# CI trust and build reporting implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Untrusted builds get a disposable home and never feed a
trusted build (#255, #258); `ci/*` statuses belong to the build
subsystem and merges can wait on named contexts (#258); a build can no
longer share the runner's source address, and reaches no port on the
runner's host but the forge's public 22, 80 and 443 (#260); a failed
build names its step, exit and duration on the CLI and the web (#266).

**Architecture:** The claim payload gains an explicit `trusted` flag and
the instance's public ssh destination (with the port when it is not
22). The runner picks the build home by trust (persistent per
repository for trusted builds, fresh and removed for untrusted ones),
keeps a loopback runner's builds off the host's loopback, and reports
the failed step on `runner done`. An nftables table on the runner host,
loaded by a oneshot unit the runner service requires, limits what the
runner's uid may reach on the host itself. The server refuses `ci/`
contexts in `status set`, restricts tree and commit reuse to trusted
builds on the same declared image, adds a `required_contexts`
repository setting that turns `require_checks` on and that `MergeGates`
treats as pending until reported, stores the failed step and reason on
the build, and cuts the log at its `$ <step>` lines for `build log
--step` and the build page.

**Tech Stack:** Go, SQLite (hand-written SQL), `html/template`, rootless
podman with pasta, nftables, systemd.

**Spec:** the issues themselves: krz/gitbay#255, #258, #260, #266 (texts
in the session's `issues.txt`), plus the decisions recorded under
"Decisions" below.

## Global Constraints

- Each MR on its own branch off `main`. Commits are signed (the repo
  refuses unsigned), messages reference issues (`Ref #N`, and
  `Closes #N` on the commit that finishes one). No attribution to any
  assistant, model or AI anywhere: commits, MR bodies, comments. No
  `Co-Authored-By` trailer.
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
  a command reading stdin needs `ReadsStdin: true`. A new or changed
  command `Summary` needs `go test ./cmd/gitbay -run TestSummariesAreCurrent -update`.
- New migrations: the highest today is 0059. Six plans are written in
  parallel, so numbers are pre-assigned: plan 1 uses 0060–0064, plan 2
  (this one) 0065–0068, plan 3 0069–0071, plan 4 0072–0074, plan 5
  0075–0077, plan 6 0078–0079. Whoever lands second renumbers to the
  next free number at execution time. Migrations come in
  `.up.sql`/`.down.sql` pairs. Hand-written SQL, no ORM. This plan uses
  one: 0065.
- Secrets travel on stdin, never argv; never logged or echoed.
- Wiki pages live in `.gitbay/wiki/` (Parity, API, Admin, Threat-Model,
  CI, Users, Performance, and the `Architecture/` folder with its
  Known-Gaps table and controls matrix). Update the page in the same MR
  that changes the behaviour it describes, and close the matching
  Known-Gaps row.
- Writing style: plain, direct, no hype; code comments match the
  surrounding density. Comments and docs state facts, never
  before/after narration.
- **Deploy order for Parts 1, 3 and 4: `make deploy` (gitbayd) before
  `make deploy-runner`.** A new runner reads `trusted` and `ssh` from the
  claim and sends `--step`/`--reason` to `runner done`; an older server
  omits the first two (the runner then treats every build as untrusted:
  no secrets, disposable home) and refuses the flags with exit 2 (the
  build stays running until the reaper fails it). An older runner
  against a newer server works unchanged. The laptop runner is a brew
  bottle built from a release tag, so it always trails the server.
- **Runner changes are validated on a scratch repository before the
  bay1 runner touches real repositories** (CLAUDE.md: every deploy that
  skipped this took CI down). The procedure is in the runbook at the end
  of this plan; Parts 1, 3 and 4 each have a validation section there.
- `cmd/gitbay-runner` tests run on macOS and Linux; nothing here may
  need podman to pass (`e2e/isolation_podman_test.go` is the only podman
  test and skips without it).

## Decisions

- **#255.** The claim carries `"trusted": true|false` with no
  `omitempty`; a runner that finds the field absent treats the build as
  untrusted. Trusted homes move from `<workdir>/home/<owner>/<name>` to
  `<workdir>/trusted-home/<owner>/<name>`, so a home written before this
  change — which untrusted builds could write — is never read again,
  even before the operator deletes it. An untrusted build's home is
  `<workdir>/build-<id>-home`, created with `os.Mkdir` (so it is new and
  empty) and removed after the build with a helper that first makes
  every directory writable (the Go module cache leaves them 0555). No
  persistent cache for untrusted builds: a fork's build downloads its
  modules each time. The runner also drops secrets for an untrusted
  build, though the server never sends any.
- **#258.** `status set` refuses any context starting with `ci/`,
  case-folded, with exit 4, before resolving the repository. Tree reuse
  (`SuccessBuildForTree`) and the cancelled-build fallback
  (`SuccessBuildFor`) consider trusted builds only, and tree reuse also
  requires the same `image:` as the job declares. The same-commit dedupe
  in `queueJobs` lets an untrusted build stand only for another
  untrusted queue: a fork head that lands on a branch by fast-forward is
  built again as trusted. `required_contexts` is a list in the
  repository's settings JSON (no migration), set by
  `repo settings require-contexts <owner/name> [<context>...]`. Setting
  a non-empty list also turns `require_checks` on, in the same settings
  update; setting it empty clears the list and leaves `require_checks`
  as it was. `require-checks off` keeps the list, which then does
  nothing until the gate is on again. `repo settings show` prints
  `require checks` and `required contexts` side by side (today it prints
  neither), and on the web settings page the required-checks box is
  ticked after contexts are saved and its hint lists them. The gate
  itself only ever reads `require_checks`. A missing required context
  makes the combined check `pending` and appears as `<context>=missing`
  in the unmet sentence and in `checks_missing`.
- **#258, default image.** Tree reuse keys on the job's declared
  `image:`. A job naming none is stored with `image = ''` and matches
  other such builds whatever the runner defaulted to; bumping a runner's
  `-image` does not invalidate them. This is documented on the CI page
  rather than fixed. Reuse is decided at queue time in `queueJobs`,
  before any runner is chosen, and the default image belongs to
  whichever runner claims the build: bay1 and the laptop runner already
  have different defaults. Reporting the resolved image on claim or
  `runner done` would record it after the fact, but a queue-time
  comparison would still have nothing to compare against, so jobs
  without an image would never be reused at all. The remedy is on the
  repository's side (name the image in `ci.yml`) or the operator's
  (`build trigger`, which never reuses, after bumping `-image`).
- **#260.** Reading the code: the bay1 runner polls `git@127.0.0.1`
  (`deploy/gitbay-runner.override.conf:78`); `buildSSH`
  (`cmd/gitbay-runner/main.go:471-486`) sends podman builds to
  `169.254.1.2`, and pasta's default gateway mapping lets a build reach
  the host's loopback, where its connections arrive from `127.0.0.1`.
  The limiter keys on the remote IP (`internal/sshd/ratelimit.go:86`,
  used at `internal/sshd/sshd.go:122`). "Runner on the public address"
  does not separate anything: a build can connect to the public address
  too, and would then share the runner's source there instead. So the
  runner stays on loopback and its builds lose loopback: under podman,
  when the runner's remote is loopback, containers run with
  `--network pasta:--no-map-gw`, and `GITBAY_SSH` is the instance's
  public destination, which the server sends in the claim as `ssh`. A
  build then reaches the host only as an internet client does.
- **#260, `GITBAY_SSH` form.** `git@<site host>` when `[ssh] port` is 22
  (or unset, which config validation treats as 22), and
  `git@<site host>:<port>` otherwise, built with `net.JoinHostPort` so
  an IPv6 literal is bracketed. hutch and orgo build
  `ssh://$GITBAY_SSH/<owner>/<name>.git`, which is a valid URL in both
  forms, so nothing changes for them on 22 and they work unchanged on
  another port. A script that runs a command uses `ssh
  ssh://$GITBAY_SSH …`, which OpenSSH accepts with or without the port;
  the Users page says so. The port is the daemon's `[ssh] port`, the
  one it listens on; an instance behind a port-mapping NAT is not
  modelled.
- **#260, host egress.** Under rootless podman with pasta, a build's
  connections are made by pasta on the host from sockets owned by the
  runner's uid (`ci-runner`), the same uid the runner's own ssh runs as.
  An nftables table (`deploy/gitbay-runner-egress.nft`, loaded by
  `gitbay-runner-egress.service`, which `gitbay-runner.service`
  requires) matches output packets with `meta skuid "ci-runner"` that
  leave through `lo` — every packet to one of the host's own addresses,
  loopback or public, does — and allows only 127.0.0.1:22 (the runner's
  poll, clone and log stream), port 53 on loopback (the host resolver
  pasta forwards a build's DNS to), and 22, 80 and 443 on the public
  addresses. Everything else on the host is rejected: the admin sshd on
  2222 on every address, and every service bound to loopback. Traffic
  to other hosts is not matched, so outbound internet stays open (a
  fork's merge request to a Go repository must fetch its modules). The
  rule applies to all builds, trusted and untrusted, and to
  `-isolation none` builds too, since they run as the same uid. uid
  alone cannot tell a build from its runner, so 127.0.0.1:22 stays open
  to the uid; `--no-map-gw` is what keeps builds off loopback. The two
  are separate layers and both ship. The runner does not start without
  the rule (`Requires=`), following `runner-podman-setup.sh`'s rule that
  a host that is not ready fails rather than runs builds unconfined;
  `make deploy-runner` loads the rule and checks, as `ci-runner`, that
  127.0.0.1:22 answers and 2222 does not, before restarting the runner.
  The laptop runner (macOS, brew) is not covered.
- **Finding for #260.** On gitbay.org the limiter's failure count is
  unreachable by an unknown key: `authenticate` admits an unknown key as
  an anonymous `register` session whenever `registration.mode` is not
  `closed` (`internal/sshd/sshd.go:139-144`), and `fail` is only called
  on the closed path (`sshd.go:145`). With registration closed,
  `authenticate` checks `allow` before it looks at the key
  (`sshd.go:123-129`), so once an address has `ssh_auth_rate` failures
  in the window every key from it is refused, the runner's included, and
  `success` (`sshd.go:149`) is never reached to clear the count; below
  the limit a success clears it. A unit test in `internal/sshd` records
  both modes (Task 3.3). No throttling test runs on production; the
  runbook measures, from inside a scratch build, the source address the
  forge sees and that 2222 and 127.0.0.1 are unreachable, and #260
  closes when that is recorded on the CI wiki page.
- **#266.** Duration is not stored: `Build.Elapsed()`
  (`internal/store/builds.go:420`) already derives it from `started_at`
  and `finished_at`. Migration 0065 adds `failed_step` (1-based, 0 for
  "no step": success, or a failure before the first step) and
  `failed_reason` (one line, at most 200 bytes). The runner writes
  `step 3/3 failed: exit 1` and reports `runner done <id> failure --step
  3 --reason 'exit 1'`; the reason is `exit <code>` for a command that
  exited and the error text otherwise (`build timed out after 45m0s`,
  `cancelled`, `git clone: exit 128`). The log format is otherwise
  unchanged: sections are cut at the `$ <step>` line the runner already
  writes before each step (`isolate.go:88`, `:186`), matched against the
  build's own `steps` in order and only at a line start, so logs of
  builds that ran before this change fold too. `build log --step`
  takes `0` (setup, before the first step), a step number, or `failed`.
  An invalid `--step` on `runner done` is recorded as 0 rather than
  refused, so a runner/server mismatch never loses an outcome.

## Order and dependencies

| # | Branch | Closes | Migration | Needs |
|---|---|---|---|---|
| 1 | `ci-untrusted-home` | #255 | — | — |
| 2 | `ci-status-trust` | #258 | — | — |
| 3 | `runner-source-address` | Ref #260 (closed by the runbook result commit on the CI page) | — | Part 1 merged (both edit `runRunnerNext`'s payload and `stepEnv`) |
| 4 | `build-failure-report` | #266 | 0065 | Part 1 merged (both change `run()`); Part 3 merged (both change `runStepsPodman`) |

#255 goes first. Parts 1–4 land and deploy in order; each runner deploy
follows the scratch validation in the runbook.

Other plans (all `docs/plans/2026-09-27-*.md`):

- Plan 4 (data-at-rest-and-backup, #273) encrypts `build_secrets`; if it
  changes `Store.BuildSecrets`, Part 1's edit to `runRunnerNext`
  (`internal/control/build.go:543-564`) conflicts textually. Whoever
  lands second rebases; no behavioural dependency.
- Plan 3 (server-hardening, #275) audits refused mutating commands; the
  `status set` refusal from Part 2 is one of them and needs nothing
  from this plan.
- Plan 5 (web-ux, #261) covers documentation drift. Two items seen here
  and left alone: `deploy/gitbay-runner.override.conf:27-30` names
  `cmc/ci-smoke`, which no longer exists; `cmd/gitbay-runner/main.go:513`
  repeats its comment line. Part 3 adds a `[Unit]` section at the top of
  the same drop-in; if plan 5 edits its comment, whoever lands second
  rebases.
- Plan 1 (credentials-and-sessions, #256) closes a removed key's
  connections; the runner's `runner log` session is one such
  connection, and no code here depends on it. Plan 1's key expiry
  (#277) may add a check to `authenticate`; Part 3's sshd test uses an
  unexpiring key and asserts only the limiter's behaviour, so it holds
  either way.

## File map

| File | Part | Responsibility |
|---|---|---|
| `internal/control/build.go` | 1, 3, 4 | claim payload `trusted`, `ssh`; `runner done` flags; `build show` fields; `build log --step/--tail`; `queueJobs` trust rule (2) |
| `internal/control/buildlog.go` (create) | 4 | `LogSection`, `SplitBuildLog`, `FailedSection`, `tailLines` |
| `internal/control/status.go` | 2 | `ci/` refusal |
| `internal/control/mr.go`, `output.go` | 2 | `require-contexts`, `MergeGates`, `GatesOut.ChecksMissing` |
| `internal/control/repo.go` | 2 | `repo settings show` prints require checks and required contexts |
| `internal/store/builds.go` | 2, 4 | trust and image on reuse; failed step columns |
| `internal/store/repos.go` | 2 | `RepoSettings.RequiredContexts` |
| `internal/store/migrations/0065_build_failure.{up,down}.sql` | 4 | columns |
| `cmd/gitbay-runner/main.go` | 1, 3, 4 | `job` fields, `buildHome`, `removeTree`, `stepEnv`, `loopbackRemote`, `buildSSH`, `buildNetwork`, `failure`, `exitReason` |
| `cmd/gitbay-runner/isolate.go` | 3, 4 | network flag; `runSteps` returns `*failure` |
| `cmd/gitbay-runner/report.go` | 4 | `doneArgs` |
| `cmd/gitbay/main.go`, `summaries_gen.go` | 2 | `require-contexts` pass-through |
| `internal/httpd/builds.go`, `settings.go` | 2, 4 | step view; settings form mapping |
| `internal/web/templates/build.html`, `mr.html`, `settings.html` | 2, 4 | steps, gates row, form |
| `internal/web/static/style.css` | 4 | `pre.buildlog` wraps; step folds |
| `e2e/readonly_test.go`, `e2e/mrweb_test.go`, `e2e/status_test.go`, `e2e/settingsweb_test.go`, `e2e/ci_test.go` | 2, 4 | contexts off `ci/`; refusal; settings; failed step |
| `internal/sshd/sshd_test.go` | 3 | limiter behaviour by registration mode |
| `deploy/gitbay-runner-egress.nft` (create), `deploy/gitbay-runner-egress.service` (create), `deploy/runner-egress-check.sh` (create) | 3 | host egress rule, its unit, the post-load check |
| `deploy/gitbay-runner.override.conf`, `deploy/runner-podman-setup.sh`, `Makefile` | 3 | runner requires the rule; nftables installed; `deploy-runner` ships, loads and checks it |
| `.gitbay/wiki/…` | all | as listed per task |

---

# Part 1: disposable home for untrusted builds (branch `ci-untrusted-home`, #255)

### Task 1.1: the claim says whether a build is trusted

**Files:**
- Modify: `internal/control/build.go:554-564` (the claim payload in `runRunnerNext`)
- Test: `internal/control/runnernext_test.go` (append)

**Interfaces:**
- Produces: the `runner next --json` payload gains `"trusted": <bool>`, always present.

- [ ] **Step 1: Write the failing test**

Append to `internal/control/runnernext_test.go`:

```go
// The claim says whether a build is trusted in so many words. A runner
// must not infer it from secrets being absent: a trusted repository with
// no secrets looks the same (#255).
func TestRunnerNextSaysWhetherTrusted(t *testing.T) {
	st, repo, uid, root, baseSHA, _ := setupOrphanRepo(t)
	for _, trusted := range []bool{true, false} {
		if _, err := st.CreateBuild(repo.ID, "unit", baseSHA, "main", "[]", "", "", trusted); err != nil {
			t.Fatal(err)
		}
		c, out := runnerCtx(st, uid, root)
		c.JSON = true
		if code := runRunnerNext(c, []string{"--untrusted"}); code != protocol.ExitOK {
			t.Fatalf("runner next: exit %d, output:\n%s", code, out.String())
		}
		want := fmt.Sprintf(`"trusted":%v`, trusted)
		if !strings.Contains(out.String(), want) {
			t.Fatalf("claim of a trusted=%v build lacks %s:\n%s", trusted, want, out.String())
		}
	}
}
```

The first loop creates and claims the trusted build; the second creates
the untrusted one, which is then the only pending build.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestRunnerNextSaysWhetherTrusted -count=1`
Expected: FAIL, `claim of a trusted=true build lacks "trusted":true`.

- [ ] **Step 3: Implement**

Replace the payload at `internal/control/build.go:554-564` with:

```go
	d := struct {
		ID     int64    `json:"id"`
		Repo   string   `json:"repo"`
		Number int64    `json:"number"`
		Job    string   `json:"job"`
		SHA    string   `json:"sha"`
		Ref    string   `json:"ref"`
		Steps  []string `json:"steps"`
		Image  string   `json:"image,omitempty"`
		// Trusted is always sent: a runner decides a build's home and
		// secrets from it, and reads a missing field as untrusted (#255).
		Trusted bool              `json:"trusted"`
		Secrets map[string]string `json:"secrets,omitempty"`
	}{ID: b.ID, Repo: repo.Path(), Number: b.Number, Job: b.Job, SHA: b.SHA, Ref: b.Ref,
		Steps: steps, Image: b.Image, Trusted: b.Trusted, Secrets: secrets}
```

- [ ] **Step 4: Run it and see it pass**

Run: `go test ./internal/control -run 'TestRunnerNext' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/build.go internal/control/runnernext_test.go
git commit -S -m "runner next: say whether the build is trusted

Ref #255"
```

### Task 1.2: the runner's build home follows trust

**Files:**
- Modify: `cmd/gitbay-runner/main.go:12-30` (imports), `:32-42` (`job`), `:315-330` (`run`), `:437-463` (comment and `buildHomeFor`), `:488-511` (`stepEnv`)
- Modify: `cmd/gitbay-runner/home_test.go` (rewrite), `cmd/gitbay-runner/env_test.go:47-60`

**Interfaces:**
- Consumes: the `trusted` claim field from Task 1.1.
- Produces:
  - `job.Trusted bool` (`json:"trusted"`)
  - `func buildHome(workdir string, j job) (string, func(), error)` — the home and a cleanup to defer; replaces `buildHomeFor`.
  - `func removeTree(dir string) error`

- [ ] **Step 1: Write the failing tests**

Replace `cmd/gitbay-runner/home_test.go` with:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A trusted build's home is its repository's, kept between builds so
// tool caches survive: the same repository gets the same directory back,
// another repository a different one (#184).
func TestTrustedHomeIsPerRepositoryAndKept(t *testing.T) {
	work := t.TempDir()
	a, done, err := buildHome(work, job{ID: 1, Repo: "alice/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("trusted home removed after its build: %v", err)
	}
	b, done, err := buildHome(work, job{ID: 2, Repo: "bob/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	if a == b {
		t.Fatalf("two repositories share a build home: %s", a)
	}
	again, done, _ := buildHome(work, job{ID: 3, Repo: "alice/app", Trusted: true})
	done()
	if again != a {
		t.Fatalf("build home moved between builds: %s then %s", a, again)
	}
	for _, dir := range []string{a, b} {
		rel, err := filepath.Rel(filepath.Join(work, "trusted-home"), dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			t.Fatalf("build home %s is not under %s/trusted-home", dir, work)
		}
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("build home mode %o, want 0700", st.Mode().Perm())
		}
	}
}

// An untrusted build gets a home of its own, outside the trusted root,
// removed when the build ends: nothing a fork's build writes reaches a
// later build of the repository (#255).
func TestUntrustedHomeIsDisposable(t *testing.T) {
	work := t.TempDir()
	trusted, done, err := buildHome(work, job{ID: 1, Repo: "alice/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	home, done, err := buildHome(work, job{ID: 2, Repo: "alice/app"})
	if err != nil {
		t.Fatal(err)
	}
	if home == trusted || strings.HasPrefix(home, filepath.Join(work, "trusted-home")) {
		t.Fatalf("untrusted build got a trusted home: %s", home)
	}
	// What the Go module cache leaves behind: read-only directories.
	cache := filepath.Join(home, "go", "pkg", "mod", "example.com", "m@v1")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go.mod"), []byte("module m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	os.Chmod(cache, 0o555)
	os.Chmod(filepath.Dir(cache), 0o555)
	done()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("untrusted home left behind: %v", err)
	}
}

// A repository path is server-validated, but a trusted home must still
// never resolve outside the runner's home root.
func TestBuildHomeRefusesTraversal(t *testing.T) {
	if _, _, err := buildHome(t.TempDir(), job{Repo: "../../etc", Trusted: true}); err == nil {
		t.Fatal("a traversing repository path produced a build home")
	}
}
```

In `cmd/gitbay-runner/env_test.go`, replace `TestStepEnvCarriesSecrets`
(lines 47-60) with:

```go
// Secrets reach a trusted build's steps and never an untrusted one's,
// whatever the claim carried: the trust flag decides, not whether any
// secrets arrived (#255).
func TestStepEnvCarriesSecrets(t *testing.T) {
	secrets := map[string]string{"TOKEN": "s3cret"}
	env := stepEnv(job{Trusted: true, Secrets: secrets}, "/tmp/buildhome", "git@x.test")
	if !containsEnv(env, "TOKEN=s3cret") {
		t.Error("a trusted build's secret did not reach the step")
	}
	for _, j := range []job{{}, {Secrets: secrets}} {
		for _, e := range stepEnv(j, "/tmp/buildhome", "git@x.test") {
			if strings.HasPrefix(e, "TOKEN=") {
				t.Errorf("a secret reached an untrusted build: %q", e)
			}
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/gitbay-runner -count=1`
Expected: build failure, `undefined: buildHome` and `unknown field Trusted in struct literal of type job`.

- [ ] **Step 3: Implement**

In `cmd/gitbay-runner/main.go`:

Add `"io/fs"` to the imports (between `"io"` and `"log"`).

Add the field to `job` (after `Image`):

```go
	Image   string            `json:"image"`
	// Trusted is false for a merge request head from a fork, and when the
	// server did not say: such a build gets no secrets and a home of its
	// own (#255).
	Trusted bool              `json:"trusted"`
	Secrets map[string]string `json:"secrets"`
```

Replace lines 315-330 of `run` (the build-home comment and the
`buildHomeFor` call) with:

```go
	home, doneHome, err := buildHome(r.workdir, j)
	if err != nil {
		log.Printf("build %d: build home: %v", j.ID, err)
		return false
	}
	defer doneHome()
```

and change line 433 to `env := stepEnv(j, home, r.buildSSH())`.

Replace lines 437-463 (the `stepEnv` comment that sits above
`buildHomeFor`, and `buildHomeFor`) with `buildHome` and `removeTree`;
the `stepEnv` comment moves to `stepEnv` in the next block:

```go
// buildHome is a build's HOME and what to do with it when the build ends.
//
// Not the workspace, which is removed after every build: the Go module
// cache and every other tool cache live under HOME. Not the runner's own
// home either, where its SSH key and credential dotfiles are.
//
// A trusted build gets its repository's home,
// <workdir>/trusted-home/<owner>/<name>, kept between builds so the
// caches survive. One per repository: shared across repositories, a step
// could poison a cache or plant a .gitconfig that another repository's
// build would honour (#184). The root is not <workdir>/home, where homes
// that untrusted builds could write were kept before #255, so none of
// those is read again.
//
// An untrusted build gets <workdir>/build-<id>-home, new and empty,
// removed when the build ends. The container mounts HOME read-write, so
// a home a fork's build could write is a cache a stranger controls
// (#255).
func buildHome(workdir string, j job) (string, func(), error) {
	if !j.Trusted {
		dir := filepath.Join(workdir, fmt.Sprintf("build-%d-home", j.ID))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", nil, err
		}
		return dir, func() {
			if err := removeTree(dir); err != nil {
				log.Printf("build %d: removing its home: %v", j.ID, err)
			}
		}, nil
	}
	root := filepath.Join(workdir, "trusted-home")
	dir := filepath.Join(root, filepath.FromSlash(j.Repo))
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", nil, fmt.Errorf("repository path %q escapes the build home root", j.Repo)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	return dir, func() {}, nil
}

// removeTree deletes dir and everything under it. os.RemoveAll alone
// fails on a directory without write permission, and the Go module cache
// makes every directory it fills read-only.
func removeTree(dir string) error {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}
```

Replace `stepEnv` (lines 488-511) with the function and the comment
that belongs to it:

```go
// stepEnv builds the environment a build step runs with. It is
// constructed, not inherited: os.Environ() would hand repository content
// the runner's entire environment, including anything an operator set on
// the service (#144).
//
// HOME is the build's home (buildHome), not the runner's own: tools read
// credentials out of dotfiles — .netrc, .npmrc, .gitconfig — and a build
// has no business finding the runner's.
//
// PATH is the one thing carried over: without it a step cannot find the
// tools the host was provisioned with.
func stepEnv(j job, home, sshDest string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"LANG=C.UTF-8",
		"CI=true",
		"GITBAY_REPO=" + j.Repo,
		"GITBAY_SHA=" + j.SHA,
		"GITBAY_REF=" + j.Ref,
		"GITBAY_JOB=" + j.Job,
		"GITBAY_SSH=" + sshDest,
	}
	// The server sends secrets only for a trusted build. The claim's
	// trust flag decides here as well, not whether any arrived (#255).
	if j.Trusted {
		for name, value := range j.Secrets {
			env = append(env, name+"="+value)
		}
	}
	return env
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go vet ./cmd/gitbay-runner && go test ./cmd/gitbay-runner -count=1`
Expected: PASS. (`TestStepEnvHomeIsNotTheWorkspace` and the other
`stepEnv` tests pass unchanged.)

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbay-runner/main.go cmd/gitbay-runner/home_test.go cmd/gitbay-runner/env_test.go
git commit -S -m "runner: disposable home for untrusted builds

A trusted build keeps its repository's home, now under
<workdir>/trusted-home; an untrusted build gets a new home removed with
the build, and no secrets whatever the claim carries.

Ref #255"
```

### Task 1.3: wiki, and the MR

**Files:**
- Modify: `.gitbay/wiki/Threat-Model.org:135-147`, `:172-180`
- Modify: `.gitbay/wiki/Admin.org:640-642`
- Modify: `.gitbay/wiki/Architecture/07-CI-and-Supply-Chain.org:30-33`, `:67`
- Modify: `.gitbay/wiki/Architecture/09-Controls.org:87`
- Modify: `.gitbay/wiki/Architecture/04-Trust-Boundaries.org` (TB7 row)
- Modify: `.gitbay/wiki/Architecture/10-Known-Gaps.org:13` (remove the #255 row)

- [ ] **Step 1: Threat-Model**

In "The CI runner", replace the sentences of the "What a build sees"
bullet from "=HOME= is a build home" to the end of the bullet with:

```org
  secrets, and nothing the operator set on the service. =HOME= is a build
  home under the runner's =-workdir=, not the runner's own home, so a
  build cannot read the =.netrc=, =.npmrc= or =.gitconfig= where tools
  keep credentials. A trusted build's home belongs to its repository
  and persists, so caches survive; an untrusted build's home is new,
  empty and removed when the build ends, so nothing a fork's build
  writes is read by a later build (krz/gitbay#255). The claim names a
  build's trust explicitly, and a runner that finds no trust flag treats
  the build as untrusted.
```

Replace the paragraph after the bullets ("Under =-isolation none=,
anything a step can do…") with:

```org
Under =-isolation none=, anything a step can do as the runner's user a
pushed =ci.yml= can do. Under podman a step is confined to its
container, the bind-mounted workspace and its build home: a trusted
build's cache is read only by later trusted builds of the same
repository, and an untrusted build's home is discarded with it. Treat
the runner host as executing untrusted code all the same: keep it off
the daemon's host where the database lives, or scope it to repositories
whose writers you trust. gitbay.org does the latter — its runner builds
only the repositories the operator names.
```

- [ ] **Step 2: Admin**

Replace `Admin.org:640-642` ("Each repository gets its own build home…")
with:

```org
A trusted build's home is its repository's, under
=<workdir>/trusted-home/<owner>/<name>=, mounted into its containers as
=HOME=: caches persist between trusted builds of one repository and are
never read by another's. An untrusted build — a merge request head from
a fork — gets =<workdir>/build-<id>-home=, new and empty, removed when
the build ends. Homes under =<workdir>/home= are from runners before
krz/gitbay#255, which shared them with untrusted builds; nothing reads
them any more, and they can be deleted.
```

- [ ] **Step 3: Architecture pages**

`07-CI-and-Supply-Chain.org`, lifecycle step 2, replace "The claim
returns id, repository, job, commit, ref, steps, image and — for
trusted builds only — the repository's secrets (=build.go=)." with
"The claim returns id, repository, job, commit, ref, steps, image, the
build's trust, and — for trusted builds only — the repository's secrets
(=build.go=)."

Replace the Build home row (line 67) with:

```org
| Build home                 | trusted: =<workdir>/trusted-home/<owner>/<name>=, one per repository, persistent; untrusted: =<workdir>/build-<id>-home=, removed with the build (=main.go=) |
```

`09-Controls.org` line 87:

```org
| Untrusted code runs isolated                | in place | rootless podman, cgroup limits; untrusted builds get a disposable home (=cmd/gitbay-runner/main.go=) |
```

`04-Trust-Boundaries.org` TB7 row, last column:

```org
| TB7 | Z5 → Z4 container                  | build steps, workspace, build home               | rootless podman, operator-provisioned image, cgroup limits; a trusted build's home is its repository's, an untrusted build's is discarded with it; the network is open (#260) |
```

`10-Known-Gaps.org`: delete the `#255` row.

- [ ] **Step 4: Verify and commit**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbay-runner ./internal/control -count=1`
Expected: PASS.

```bash
git add .gitbay/wiki
git commit -S -m "wiki: trusted and untrusted build homes

Closes #255"
```

- [ ] **Step 5: MR**

```bash
git push -u origin ci-untrusted-home
gitbay mr create --source ci-untrusted-home --target main --title "runner: disposable home for untrusted builds"
```

Body (via `--file -` from a file written with the Write tool): what
changed, the deploy order (gitbayd before the runner), and a pointer to
runbook sections R1 and R2. After CI is green and the runbook's R2
validation passed on the scratch repository:
`gitbay mr merge <n> --strategy ff`, delete the branch both places.

---

# Part 2: `ci/` statuses, trusted reuse, required contexts (branch `ci-status-trust`, #258)

### Task 2.1: `status set` refuses `ci/`

**Files:**
- Modify: `internal/control/status.go:15-27` (registration), `:68-70` (after the usage check)
- Create: `internal/control/status_test.go`
- Modify: `e2e/readonly_test.go:75`, `e2e/mrweb_test.go:275`, `e2e/status_test.go` (after line 49)
- Modify: `cmd/gitbay/summaries_gen.go` only if the summary changes (it does not here)

**Interfaces:**
- Produces: `status set … --context ci/…` exits 4 (`protocol.ExitDenied`) with a message containing `reserved`.

- [ ] **Step 1: Write the failing test**

Create `internal/control/status_test.go`:

```go
package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// ci/<job> statuses are the build subsystem's. A writer who could post
// one could mark ci/test green on their own head before, or instead of,
// the build (#258).
func TestStatusSetRefusesReservedContext(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, ctx := range []string{"ci/test", "CI/test", "ci/"} {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		code := Dispatch(c, []string{"status", "set", repo.Path(), "abc1234", "--context", ctx, "--state", "success"})
		if code != protocol.ExitDenied || !strings.Contains(errOut.String(), "reserved") {
			t.Errorf("--context %s: exit %d, %s", ctx, code, errOut.String())
		}
	}
	if has, err := st.RepoHasStatuses(repo.ID); err != nil || has {
		t.Fatalf("a refused status was stored: %v %v", has, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestStatusSetRefusesReservedContext -count=1`
Expected: FAIL, exit 3 (`no commit abc1234`), since today the context is
accepted and the missing commit is what stops it.

- [ ] **Step 3: Implement**

In the registration, change the `--context` flag and the example:

```go
			{"--context", "<c>", "the check this status reports for; ci/ is reserved for the instance's builds", ""},
```

```go
		Examples: []string{
			"status set krz/gitbay a1b2c3d --context ext/lint --state success",
		},
```

After the usage check at `status.go:68-70`, before the `--url` check:

```go
	// ci/<job> statuses are the build subsystem's: queued, reused,
	// skipped and finished by the server itself. A writer who could post
	// one could mark ci/test green on their own head before, or instead
	// of, the build (#258). Case-folded, so CI/test is no way around it.
	if strings.HasPrefix(strings.ToLower(context), "ci/") {
		return c.fail(protocol.ExitDenied, "the ci/ prefix is reserved for the instance's builds; report under another name, such as ext/%s",
			strings.TrimPrefix(strings.ToLower(context), "ci/"))
	}
```

- [ ] **Step 4: Run it and see it pass**

Run: `go test ./internal/control -run 'TestStatusSet|TestHelp' -count=1`
Expected: PASS.

- [ ] **Step 5: e2e callers off `ci/`, and the refusal over SSH**

`e2e/readonly_test.go:75`: `"--context", "ci/x"` → `"--context", "ext/x"`.
`e2e/mrweb_test.go:275`: `"--context", "ci/test"` → `"--context", "ext/test"`.

In `e2e/status_test.go`, after the reader-denied check (line 49), add:

```go
	// ci/ is the instance's own: a writer is refused it (#258).
	if _, errOut, code := inst.ssh(t, bobKey, "", "status", "set", "alice/svc", head, "--context", "ci/build", "--state", "success"); code != 4 || !strings.Contains(errOut, "reserved") {
		t.Fatalf("writer posted a ci/ status: exit %d, %s", code, errOut)
	}
```

Run: `go test ./e2e -run TestCommitStatuses -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/control/status.go internal/control/status_test.go e2e/readonly_test.go e2e/mrweb_test.go e2e/status_test.go
git commit -S -m "status set: ci/ is reserved for the instance's builds

Ref #258"
```

### Task 2.2: reuse only trusted results on the same image

**Files:**
- Modify: `internal/store/builds.go:453-477` (`SuccessBuildForTree`, `SuccessBuildFor`)
- Modify: `internal/control/build.go:856-864` (`queueJobs`)
- Modify: `internal/store/builds_test.go:241-249` (`TestSuccessBuildForTree` call sites) and append a test
- Test: `internal/control/build_test.go` (append)

**Interfaces:**
- Produces: `func (s *Store) SuccessBuildForTree(repoID int64, tree, job, image string) (Build, bool, error)` — trusted builds only, same image. `SuccessBuildFor` keeps its signature and considers trusted builds only.

- [ ] **Step 1: Write the failing tests**

In `internal/store/builds_test.go`, add `""` as the fourth argument to
the three `SuccessBuildForTree` calls in `TestSuccessBuildForTree`, then
append:

```go
// A result stands for another commit only when it came from a trusted
// build on the same image: a fork's green build, or one on an image the
// job has since left, proves nothing about the repository's own (#258).
func TestSuccessReuseNeedsTrustAndImage(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, _ := s.CreateUser("cmc", true)
	repoID, _ := s.CreateRepo("user", uid, "app", "public")
	for _, b := range []struct {
		sha, image string
		trusted    bool
	}{
		{"aaa", "", false},
		{"bbb", "localhost/old:1", true},
	} {
		if _, err := s.CreateBuild(repoID, "unit", b.sha, "main", `["true"]`, b.image, "tree1", b.trusted); err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := s.ClaimBuild([]int64{repoID}, true)
		if err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if err := s.FinishBuild(claimed.ID, "success"); err != nil {
			t.Fatal(err)
		}
	}
	if prev, ok, _ := s.SuccessBuildForTree(repoID, "tree1", "unit", ""); ok {
		t.Fatalf("reused build %d: untrusted, or on another image", prev.Number)
	}
	if prev, ok, _ := s.SuccessBuildForTree(repoID, "tree1", "unit", "localhost/old:1"); !ok || prev.SHA != "bbb" {
		t.Fatalf("trusted build on the same image not found: ok=%v prev=%+v", ok, prev)
	}
	if _, ok, _ := s.SuccessBuildFor(repoID, "aaa", "unit"); ok {
		t.Error("an untrusted success stood for its commit")
	}
}
```

Append to `internal/control/build_test.go`:

```go
// A fork's green build of a commit does not stand for the repository's
// own: the same commit landing on a branch, or a commit with the same
// tree, is built again as trusted (#258).
func TestQueueBranchBuildsRebuildsWhatOnlyAForkBuilt(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    steps:\n      - echo hi\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	first := strings.TrimSpace(git(src, "rev-parse", "HEAD"))
	git(src, "commit", "-q", "--allow-empty", "-m", "same tree")
	second := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	// A fork's merge request head, built untrusted and green.
	QueueMRBuilds(st, root, "https://x.test", repo, uid, 1, first)
	b, ok, err := st.ClaimBuild([]int64{repo.ID}, true)
	if err != nil || !ok || b.Trusted {
		t.Fatalf("claim: ok=%v trusted=%v err=%v", ok, b.Trusted, err)
	}
	if err := st.FinishBuild(b.ID, "success"); err != nil {
		t.Fatal(err)
	}

	// The same commit lands on main, then a commit with the same tree.
	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "main", "", first, time.Now())
	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "main", first, second, time.Now())
	pending, _ := st.ListBuilds(repo.ID, store.BuildFilter{Status: "pending"}, 10)
	if len(pending) != 2 {
		t.Fatalf("queued %d builds, want 2 (one per commit): %+v", len(pending), pending)
	}
	for _, p := range pending {
		if !p.Trusted {
			t.Errorf("build %d queued untrusted on a branch push", p.Number)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/store -run 'TestSuccessBuildForTree|TestSuccessReuse' -count=1`
Expected: build failure (`too many arguments in call to s.SuccessBuildForTree`).

Run: `go test ./internal/control -run TestQueueBranchBuildsRebuildsWhatOnlyAForkBuilt -count=1`
Expected: FAIL, `queued 0 builds, want 2`.

- [ ] **Step 3: Store**

Replace `internal/store/builds.go:453-477` with:

```go
// SuccessBuildForTree finds a passed build of the job for a tree rather
// than a commit: a rebase that changes nothing in the tree has already
// been built (#177). Only a trusted build on the image the job names
// counts: a fork's result, or one from an image the job has left, does
// not stand for the repository's own (#258). A job naming no image
// matches builds that named none, whichever default the runner used;
// the CI wiki page says so. An empty tree never matches.
func (s *Store) SuccessBuildForTree(repoID int64, tree, job, image string) (Build, bool, error) {
	if tree == "" {
		return Build{}, false, nil
	}
	b, err := scanBuild(s.DB.QueryRow(buildSelect+
		" WHERE repo_id = ? AND tree = ? AND job = ? AND image = ? AND trusted = 1 AND status = 'success'"+
		" ORDER BY number DESC LIMIT 1", repoID, tree, job, image))
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, false, nil
	}
	return b, err == nil, err
}

// SuccessBuildFor finds a passed trusted build of the commit for the job,
// on any ref: what a cancelled duplicate can point back at.
func (s *Store) SuccessBuildFor(repoID int64, sha, job string) (Build, bool, error) {
	b, err := scanBuild(s.DB.QueryRow(buildSelect+
		" WHERE repo_id = ? AND sha = ? AND job = ? AND trusted = 1 AND status = 'success' ORDER BY number DESC LIMIT 1", repoID, sha, job))
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, false, nil
	}
	return b, err == nil, err
}
```

- [ ] **Step 4: `queueJobs`**

Replace `internal/control/build.go:856-859` with:

```go
		// A build of this commit that passed, or is queued or running,
		// stands for it — unless this queue is trusted and that build was
		// not: a fork's head that lands on a branch is built again as the
		// repository's own (#258).
		if b, ok := built[j.Name]; ok && (b.Trusted || !trusted) &&
			(b.Status == "success" || b.Status == "pending" || b.Status == "running") {
			continue
		}
		if prev, ok, _ := st.SuccessBuildForTree(repo.ID, tree, j.Name, j.Image); ok && prev.SHA != sha {
```

(the body of the `if prev, ok` block is unchanged.)

- [ ] **Step 5: Run the tests and see them pass**

Run: `go vet ./... && go test ./internal/store ./internal/control ./internal/hookd ./internal/ci -count=1`
Expected: PASS. `TestPushShapes` (hookd) has no row where a fork's
build lands on a branch, so its table is unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/store/builds.go internal/store/builds_test.go internal/control/build.go internal/control/build_test.go
git commit -S -m "ci: reuse only trusted results on the job's image

Ref #258"
```

### Task 2.3: required contexts

**Files:**
- Modify: `internal/store/repos.go:25-37` (`RepoSettings`)
- Modify: `internal/control/output.go:59-60` (`GatesOut`)
- Modify: `internal/control/mr.go:45-49` (registration), after `runRequireChecks` (`:326-341`), `:1579-1603` (`MergeGates` checks block)
- Modify: `internal/control/repo.go:710-717` (`repo settings show`)
- Modify: `internal/control/checksgate_test.go` (helper refactor, two tests)
- Test: `internal/control/mr_test.go` (append)
- Modify: `cmd/gitbay/main.go:580` (add a `pass`), `cmd/gitbay/summaries_gen.go` (regenerated)
- Modify: `internal/httpd/settings.go:106-107`, `:228-229`; `internal/web/templates/settings.html:73-78` (the require-checks form, its hint at line 75); `internal/web/templates/mr.html:124`
- Modify: `internal/httpd/mrpage_test.go` (`TestMRGatesRender`), `e2e/settingsweb_test.go:58`

**Interfaces:**
- Produces:
  - `RepoSettings.RequiredContexts []string` (`json:"required_contexts,omitempty"`)
  - `GatesOut.ChecksMissing []string` (`json:"checks_missing,omitempty"`)
  - command `repo settings require-contexts <owner/name> [<context>...]`:
    a non-empty list is stored and sets `RequireChecks = true` in the
    same update; an empty list clears the contexts and leaves
    `RequireChecks` alone.
  - `repo settings show` human output gains `require checks` and
    `required contexts` rows (JSON already carries `require_checks` and
    gains `required_contexts`).

- [ ] **Step 1: Write the failing tests**

In `internal/control/checksgate_test.go`, replace `gatesForHeadSeeded`
(lines 20-73) with a general helper and a thin wrapper:

```go
func gatesForHeadSeeded(t *testing.T, ciYML string, seed bool) GatesOut {
	return gatesFor(t, ciYML, nil, func(st *store.Store, repoID, uid int64, targetSHA, _ string) {
		if !seed {
			return
		}
		if err := st.SetCommitStatus(repoID, targetSHA, "lint", "success", "", "", uid); err != nil {
			t.Fatal(err)
		}
	})
}

// gatesFor builds a repository with require_checks on and set applied to
// its settings, a bare dir holding the given .gitbay/ci.yml (empty
// string for none), and one MR; seed records statuses before the gates
// are computed.
func gatesFor(t *testing.T, ciYML string, set func(*store.RepoSettings),
	seed func(st *store.Store, repoID, uid int64, targetSHA, headSHA string)) GatesOut {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) {
		s.RequireChecks = true
		if set != nil {
			set(s)
		}
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repo.ID)
	if err != nil {
		t.Fatal(err)
	}

	git := gitRunner(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "README"), []byte("x\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	targetSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))
	git(src, "checkout", "-q", "-b", "feature")
	if ciYML != "" {
		os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
		os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(ciYML), 0o644)
	}
	os.WriteFile(filepath.Join(src, "README"), []byte("y\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "change")
	headSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", headSHA, "md", false); err != nil {
		t.Fatal(err)
	}
	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(st, repo.ID, uid, targetSHA, headSHA)
	}
	g, err := MergeGates(st, repo, mr, dir, targetSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
```

Append to the same file (add `"slices"` to its imports):

```go
// A required context that has not reported holds the merge as pending,
// even when every status that did report is green (#258).
func TestRequiredContextMissingIsPending(t *testing.T) {
	g := gatesFor(t, "", func(s *store.RepoSettings) { s.RequiredContexts = []string{"ext/deploy", "lint"} },
		func(st *store.Store, repoID, uid int64, _, headSHA string) {
			if err := st.SetCommitStatus(repoID, headSHA, "lint", "success", "", "", uid); err != nil {
				t.Fatal(err)
			}
		})
	if g.Checks != "pending" || !slices.Equal(g.ChecksMissing, []string{"ext/deploy"}) {
		t.Fatalf("checks %q, missing %v", g.Checks, g.ChecksMissing)
	}
	if !checksUnmet(g) || !strings.Contains(strings.Join(g.Unmet, "\n"), "ext/deploy=missing") {
		t.Fatalf("unmet: %v", g.Unmet)
	}
}

// Every required context reported green: nothing is held.
func TestRequiredContextsReportedPass(t *testing.T) {
	g := gatesFor(t, "", func(s *store.RepoSettings) { s.RequiredContexts = []string{"lint"} },
		func(st *store.Store, repoID, uid int64, _, headSHA string) {
			if err := st.SetCommitStatus(repoID, headSHA, "lint", "success", "", "", uid); err != nil {
				t.Fatal(err)
			}
		})
	if checksUnmet(g) || len(g.ChecksMissing) != 0 || g.Checks != "success" {
		t.Fatalf("checks %q, missing %v, unmet %v", g.Checks, g.ChecksMissing, g.Unmet)
	}
}
```

Append to `internal/control/mr_test.go` (add `"slices"` to its imports;
`mrTestCtx` is that file's helper):

```go
// require-contexts stores a deduplicated list and turns require_checks
// on with it; an empty list clears the contexts and leaves
// require_checks as it was. A context with whitespace is refused (#258).
func TestRequireContextsSetsAndClears(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	alice := store.User{ID: uid, Username: "alice"}
	dispatch := func(args ...string) int {
		t.Helper()
		c, _, _ := mrTestCtx(st, alice)
		return Dispatch(c, args)
	}
	contexts := func(names ...string) int {
		t.Helper()
		return dispatch(append([]string{"repo", "settings", "require-contexts", repo.Path()}, names...)...)
	}
	settings := func() store.RepoSettings {
		t.Helper()
		got, err := st.RepoByID(repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Settings
	}

	if settings().RequireChecks {
		t.Fatal("require_checks on in a new repository")
	}
	if code := contexts("lint", "ext/deploy", "lint"); code != protocol.ExitOK {
		t.Fatalf("set: exit %d", code)
	}
	if s := settings(); !slices.Equal(s.RequiredContexts, []string{"lint", "ext/deploy"}) || !s.RequireChecks {
		t.Fatalf("stored %v, require_checks %v; want [lint ext/deploy], on", s.RequiredContexts, s.RequireChecks)
	}
	if code := contexts("bad context"); code != protocol.ExitUsage {
		t.Fatalf("a context with a space: exit %d", code)
	}
	if code := contexts(); code != protocol.ExitOK {
		t.Fatalf("clear: exit %d", code)
	}
	if s := settings(); len(s.RequiredContexts) != 0 || !s.RequireChecks {
		t.Fatalf("after clearing: contexts %v, require_checks %v; want none, still on", s.RequiredContexts, s.RequireChecks)
	}
	if code := dispatch("repo", "settings", "require-checks", repo.Path(), "off"); code != protocol.ExitOK {
		t.Fatalf("require-checks off: exit %d", code)
	}
	if code := contexts(); code != protocol.ExitOK {
		t.Fatalf("clear again: exit %d", code)
	}
	if settings().RequireChecks {
		t.Fatal("clearing the list turned require_checks on")
	}
}

// settings show prints the checks gate beside the contexts it waits for,
// so a list that turned the gate on is visible where the gate is (#258).
func TestSettingsShowRequiredContexts(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	alice := store.User{ID: uid, Username: "alice"}
	c, _, _ := mrTestCtx(st, alice)
	if code := Dispatch(c, []string{"repo", "settings", "require-contexts", repo.Path(), "ext/deploy", "lint"}); code != protocol.ExitOK {
		t.Fatalf("require-contexts: exit %d", code)
	}
	c, out, _ := mrTestCtx(st, alice)
	if code := Dispatch(c, []string{"repo", "settings", "show", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("settings show: exit %d", code)
	}
	got := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"require checks true", "required contexts ext/deploy, lint"} {
		if !strings.Contains(got, want) {
			t.Errorf("settings show lacks %q:\n%s", want, out.String())
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run 'TestRequire|TestRequiredContext|TestSettingsShowRequiredContexts' -count=1`
Expected: build failure (`s.RequiredContexts undefined`).

- [ ] **Step 3: Store and output types**

`internal/store/repos.go`, in `RepoSettings` after `RequireChecks`:

```go
	RequireChecks        bool     `json:"require_checks,omitempty"`
	// RequiredContexts are statuses require_checks waits for whether or
	// not they have reported; one that has not is pending. Setting a
	// non-empty list turns RequireChecks on (#258).
	RequiredContexts     []string `json:"required_contexts,omitempty"`
```

`internal/control/output.go`, in `GatesOut` after `Checks`:

```go
	Checks             string      `json:"checks,omitempty"` // combined status; "" when none reported
	ChecksMissing      []string    `json:"checks_missing,omitempty"` // required contexts not reported
```

- [ ] **Step 4: The command**

Registration, after `require-checks` at `mr.go:49`:

```go
	register(Command{Path: []string{"repo", "settings", "require-contexts"},
		Summary:  "name the statuses the checks gate waits for, and turn the gate on",
		Usage:    "repo settings require-contexts <owner/name> [<context>...] (none clears the list)",
		Examples: []string{"repo settings require-contexts krz/gitbay ci/build ci/test"},
		Run:      runRequireContexts})
```

After `runRequireChecks`:

```go
// maxRequiredContexts bounds the list: a gate naming more checks than
// this is a configuration mistake.
const maxRequiredContexts = 20

func runRequireContexts(c *Ctx, args []string) int {
	if len(args) < 1 {
		return c.usage()
	}
	var contexts []string
	for _, ctx := range args[1:] {
		if ctx == "" || len(ctx) > 100 || strings.ContainsAny(ctx, " \t\r\n") {
			return c.fail(protocol.ExitUsage, "a context is 1 to 100 characters with no whitespace: %q", ctx)
		}
		if !slices.Contains(contexts, ctx) {
			contexts = append(contexts, ctx)
		}
	}
	if len(contexts) > maxRequiredContexts {
		return c.fail(protocol.ExitUsage, "at most %d required contexts", maxRequiredContexts)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	// Naming contexts asks for the gate, so it turns require_checks on in
	// the same update. Clearing the list leaves the gate as it was.
	s, err := c.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) {
		s.RequiredContexts = contexts
		if len(contexts) > 0 {
			s.RequireChecks = true
		}
	})
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		if len(contexts) > 0 {
			fmt.Fprintf(w, "required contexts on %s: %s; require_checks on\n", repo.Path(), strings.Join(contexts, ", "))
			return
		}
		gate := "off"
		if s.RequireChecks {
			gate = "on"
		}
		fmt.Fprintf(w, "required contexts cleared on %s; require_checks %s\n", repo.Path(), gate)
	})
}
```

- [ ] **Step 5: `MergeGates`**

Replace `mr.go:1579-1603` (the checks block, from the comment through
the end of `if set.RequireChecks { … }`) with:

```go
	// Checks: with require_checks, every status the head carries must be
	// green, a head something was going to report on must carry some, and
	// every required context must have reported: one that has not is
	// pending whatever the others say (#258). Setting contexts turns
	// require_checks on; turned off again, the list is kept and unread.
	statuses, err := st.ListCommitStatuses(repo.ID, headSHA)
	if err != nil {
		return g, err
	}
	g.Checks = store.CombinedStatus(statuses)
	if set.RequireChecks {
		reported := map[string]bool{}
		for _, s := range statuses {
			reported[s.Context] = true
		}
		for _, want := range set.RequiredContexts {
			if !reported[want] {
				g.ChecksMissing = append(g.ChecksMissing, want)
			}
		}
		if len(g.ChecksMissing) > 0 && (g.Checks == "" || g.Checks == "success") {
			g.Checks = "pending"
		}
		switch g.Checks {
		case "success":
		case "":
			if checksExpected(st, repo.ID, dir, headSHA) {
				g.Unmet = append(g.Unmet, fmt.Sprintf("%s requires green checks and none were reported on %.10s", repo.Path(), headSHA))
			}
		default:
			var bad []string
			for _, st := range statuses {
				if st.State != "success" {
					bad = append(bad, st.Context+"="+st.State)
				}
			}
			for _, m := range g.ChecksMissing {
				bad = append(bad, m+"=missing")
			}
			g.Unmet = append(g.Unmet, fmt.Sprintf("%s requires green checks; %.10s has %s", repo.Path(), headSHA, strings.Join(bad, ", ")))
		}
	}
```

`repo.go:710-717`, the `repo settings show` fields: today they print
neither the checks gate nor anything about it. Add two rows after
`"require mr"` (line 713), so the gate and the list that turned it on
read together:

```go
			"require mr", strconv.FormatBool(repo.Settings.RequireMR),
			"require checks", strconv.FormatBool(repo.Settings.RequireChecks),
			"required contexts", strings.Join(repo.Settings.RequiredContexts, ", "),
```

(`fields` skips a row whose value is empty, so a repository with no
contexts shows no `required contexts` row.)

- [ ] **Step 6: Run the control tests**

Run: `go vet ./... && go test ./internal/control ./internal/store -count=1`
Expected: PASS.

- [ ] **Step 7: CLI, web, summaries**

`cmd/gitbay/main.go`, after the `require-checks` line (580):

```go
			pass("require-contexts", passOpts{server: []string{"repo", "settings", "require-contexts"}, needsRepo: true}),
```

Run: `go test ./cmd/gitbay -run TestSummariesAreCurrent -update -count=1 && go test ./cmd/gitbay -count=1`
Expected: PASS; `summaries_gen.go` gains the `repo settings require-contexts` line.

`internal/httpd/settings.go`, in `settingsSubmit` after the
`require-checks` case:

```go
	case "require-contexts":
		argv = append([]string{"repo", "settings", "require-contexts", repo}, strings.Fields(v("contexts"))...)
```

and in `fieldLabel` after `require-checks`:

```go
	case "require-contexts":
		return "required contexts"
```

`internal/web/templates/settings.html`: the require-checks hint (line
75) names the contexts the gate waits for, so a box ticked by saving
contexts says why:

```html
  <div><label for="require-checks">Required checks</label><p class="hint">Requires CI to succeed.{{with .Repo.Settings.RequiredContexts}} Also waits for {{range $i, $c := .}}{{if $i}}, {{end}}<code>{{$c}}</code>{{end}} until they report{{if not $.Repo.Settings.RequireChecks}}, once this is on{{end}}.{{end}}</p></div>
```

and after the `require-checks` form (line 78):

```html
<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="require-contexts">
  <div><label for="contexts">Required contexts</label><p class="hint">Statuses the checks gate waits for until they report, separated by spaces. Saving any turns required checks on; saving none leaves it as it is.</p></div>
  <div><input type="text" id="contexts" name="contexts" value="{{range $i, $c := .Repo.Settings.RequiredContexts}}{{if $i}} {{end}}{{$c}}{{end}}" autocomplete="off"></div>
  <div><button type="submit" class="btn">Save</button></div>
</form>
```

`internal/web/templates/mr.html`, after the `OwnersOutstanding` line
(124):

```html
    {{range .ChecksMissing}}<p class="row none">waiting on <code>{{.}}</code>, not yet reported</p>{{end}}
```

In `internal/httpd/mrpage_test.go` `TestMRGatesRender`, add after the
first `for` loop:

```go
	if out := render(&control.GatesOut{Checks: "pending", ChecksMissing: []string{"ext/deploy"}}); !strings.Contains(out, "waiting on <code>ext/deploy</code>") {
		t.Errorf("missing required context not rendered:\n%s", out)
	}
```

In `e2e/settingsweb_test.go`, replace line 58,
`post(url.Values{"field": {"require-checks"}, "require-checks": {"on"}})`,
with a contexts post, so the `"require_checks":true` the test already
expects from `settings show --json` (line 69) now comes from the
contexts turning the gate on:

```go
	// Saving required contexts turns the checks gate on, and the page
	// shows it ticked with the contexts in its hint (#258).
	if body := post(url.Values{"field": {"require-contexts"}, "contexts": {"ext/deploy lint"}}); !strings.Contains(body, `id="require-checks" name="require-checks" value="on" checked`) ||
		!strings.Contains(body, `Also waits for <code>ext/deploy</code>, <code>lint</code> until they report.`) {
		t.Fatalf("required contexts did not show as turning required checks on:\n%s", body)
	}
```

and add `` `"required_contexts":["ext/deploy","lint"]` `` to the
`settings show --json` want list at line 69.

Run: `go test ./internal/httpd ./internal/web -count=1 && go test ./e2e -run TestRepoSettingsWeb -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/repos.go internal/control cmd/gitbay internal/httpd internal/web e2e/settingsweb_test.go
git commit -S -m "repo settings: required contexts turn the checks gate on, pending until reported

Ref #258"
```

### Task 2.4: wiki, and the MR

**Files:**
- Modify: `.gitbay/wiki/API.org:100-122`, `.gitbay/wiki/CI.org:5-9`, `.gitbay/wiki/Users.org:542-545`, `.gitbay/wiki/Parity.org` (settings table near line 194)
- Modify: `.gitbay/wiki/Architecture/07-CI-and-Supply-Chain.org:57`, `09-Controls.org:39`, `:92`, `10-Known-Gaps.org` (remove the #258 row)

- [ ] **Step 1: API.org**

Change the two example lines to `--context ext/build`, and after the
paragraph ending "…Each report also emits a =status= event to
webhooks." add:

```org
Contexts starting with =ci/= are the instance's own: its builds queue,
reuse, skip and finish them, and =status set= refuses them with exit 4,
so a writer cannot mark =ci/test= green on a head the build has not
passed. Report under another prefix, such as =ext/=.

=repo settings require-contexts <repo> ext/deploy ci/test= names
statuses the checks gate waits for whether or not they have reported:
one that has not is =pending=, and =mr show= lists it as
=ext/deploy=missing=. Naming any context turns =require-checks= on;
with no contexts the command clears the list and leaves
=require-checks= as it was. =require-checks off= keeps the list, which
waits for nothing until the gate is on again. =repo settings show=
prints both.
```

- [ ] **Step 2: CI.org**

In the *Dedupe* bullet, after "…naming the build it came from (#177).",
add: "Only a trusted build counts, and for tree reuse only one on the
image the job names: a fork's green build does not stand for the
repository's own, so its commit is built again when it lands on a
branch (#258). A job that names no =image:= is compared as naming
none: its reuse does not notice the runner's default image changing,
because reuse is decided when the push is queued, before any runner
claims the build, and runners can differ in their default. Name the
image in =ci.yml= to tie reuse to it; after an operator changes a
runner's =-image=, =build trigger= builds a job afresh, since a
triggered build is never reused."

- [ ] **Step 3: Users.org**

After "…a =ci/<job>= commit status, which =repo settings
require-checks= can gate merges on." add: "=repo settings
require-contexts= names statuses the gate waits for until they report,
and turns the gate on."

- [ ] **Step 4: Parity.org**

After the `require codeowners` row:

```org
| require contexts            | yes | yes | no  |
```

- [ ] **Step 5: Architecture**

`07-CI-and-Supply-Chain.org:57`:

```org
| =status set=                       | write on the repository; =ci/*= contexts refused (=status.go=) |
```

and in lifecycle step 1 replace "or =success= copied from an earlier
build of the same tree (#177)." with "or =success= copied from an
earlier trusted build of the same tree on the same image (#177, #258)."

`09-Controls.org:39`:

```org
| Merge gates                                 | in place | =MergeGates=; =ci/*= statuses written only by the build subsystem; required contexts |
```

`09-Controls.org:92`:

```org
| Build results reused only across equal trust | in place | =SuccessBuildForTree=, =SuccessBuildFor= (=internal/store/builds.go=) |
```

`10-Known-Gaps.org`: delete the `#258` row.

- [ ] **Step 6: Commit and MR**

```bash
git add .gitbay/wiki
git commit -S -m "wiki: reserved ci/ statuses, trusted reuse, required contexts

Closes #258"
git push -u origin ci-status-trust
gitbay mr create --source ci-status-trust --target main --title "ci: reserve ci/ statuses; reuse only trusted results; required contexts"
```

No runner change: this part deploys with `make deploy` alone. Merge
with `--strategy ff` once CI is green; delete the branch both places.

---

# Part 3: separate the runner's source address from its builds, and limit what builds reach on its host (branch `runner-source-address`, #260)

### Task 3.1: the claim carries the instance's public ssh destination

**Files:**
- Modify: `internal/control/build.go` (imports; the payload from Task 1.1; a helper beside `runRunnerNext`)
- Test: `internal/control/runnernext_test.go` (append)

**Interfaces:**
- Produces: `runner next --json` payload `"ssh": "git@<site host>"`, or
  `"git@<site host>:<port>"` when `[ssh] port` is neither 0 nor 22;
  omitted when `site_url` is empty.

- [ ] **Step 1: Write the failing test**

```go
// A build on the daemon's own host is given the public destination, not
// the loopback address its runner polls. The port rides along only when
// it is not 22, so ssh://$GITBAY_SSH/<owner>/<name>.git is a valid URL
// either way (#260).
func TestRunnerNextCarriesPublicSSH(t *testing.T) {
	st, repo, uid, root, baseSHA, _ := setupOrphanRepo(t)
	for _, tc := range []struct {
		port int
		want string
	}{
		{0, `"ssh":"git@x.test"`},
		{22, `"ssh":"git@x.test"`},
		{2022, `"ssh":"git@x.test:2022"`},
	} {
		if _, err := st.CreateBuild(repo.ID, "unit", baseSHA, "main", "[]", "", "", true); err != nil {
			t.Fatal(err)
		}
		c, out := runnerCtx(st, uid, root) // site_url https://x.test
		c.Cfg.SSH.Port = tc.port
		c.JSON = true
		if code := runRunnerNext(c, nil); code != protocol.ExitOK {
			t.Fatalf("port %d: runner next: exit %d, output:\n%s", tc.port, code, out.String())
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("port %d: claim lacks %s:\n%s", tc.port, tc.want, out.String())
		}
	}
}
```

Each pass queues one build and claims it, as in Task 1.1's test.
`runnerCtx` builds its `config.Config` by hand, so `SSH.Port` starts at
0; config validation refuses 0 on a real instance (`config.go:367`),
and 0 is read as 22.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/control -run TestRunnerNextCarriesPublicSSH -count=1`
Expected: FAIL, `port 0: claim lacks "ssh":"git@x.test"`.

- [ ] **Step 3: Implement**

Add `"net"` to `build.go`'s imports (`strconv` is already there), and
after `maxOrphanSkip`:

```go
// publicSSH is the instance's ssh destination as anyone outside reaches
// it. A runner on the daemon's own host polls over loopback and hands
// its builds this instead, so no build connects from the runner's source
// address (#260). The port is added only when it is not 22: hutch and
// orgo build ssh://$GITBAY_SSH/... URLs, valid in both forms. Empty when
// site_url is not set.
func publicSSH(c *Ctx) string {
	host := c.Cfg.SiteHost()
	if host == "" {
		return ""
	}
	if p := c.Cfg.SSH.Port; p != 0 && p != 22 {
		return "git@" + net.JoinHostPort(host, strconv.Itoa(p))
	}
	return "git@" + host
}
```

In the payload struct add, after `Trusted`:

```go
		// SSH is the instance's public destination for the build's
		// GITBAY_SSH when its runner polls over loopback (#260).
		SSH string `json:"ssh,omitempty"`
```

and `SSH: publicSSH(c),` in the literal.

- [ ] **Step 4: Run it and see it pass**

Run: `go test ./internal/control -run TestRunnerNext -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/build.go internal/control/runnernext_test.go
git commit -S -m "runner next: send the instance's public ssh destination

Ref #260"
```

### Task 3.2: a loopback runner's builds get no host loopback

**Files:**
- Modify: `cmd/gitbay-runner/main.go:32-42` (`job`), `:433` (`stepEnv` call), `:465-486` (`buildSSH`)
- Modify: `cmd/gitbay-runner/isolate.go:150-154` (podman run args)
- Modify: `cmd/gitbay-runner/env_test.go:190-212`

**Interfaces:**
- Consumes: the `ssh` claim field from Task 3.1.
- Produces:
  - `job.SSH string` (`json:"ssh"`)
  - `func (r *runner) loopbackRemote() bool`
  - `func (r *runner) buildSSH(public string) string` (was `buildSSH()`)
  - `func (r *runner) buildNetwork() []string`

- [ ] **Step 1: Write the failing tests**

Replace `TestStepEnvCarriesInstanceAddress` (`env_test.go:190-212`) with:

```go
// A build that talks back to the instance needs an address that works
// from where it runs. A runner polling over loopback keeps its podman
// builds off the host's loopback, so they get the instance's public
// destination from the claim, port included when it is not 22; any other
// remote is used as it is (#260).
func TestStepEnvCarriesInstanceAddress(t *testing.T) {
	env := stepEnv(job{}, "/tmp/buildhome", "git@gitbay.org")
	if !containsEnv(env, "GITBAY_SSH=git@gitbay.org") {
		t.Errorf("GITBAY_SSH missing: %q", env)
	}
	for _, tc := range []struct{ remote, isolation, public, want string }{
		{"git@127.0.0.1", isolationNone, "git@gitbay.org", "git@127.0.0.1"},
		{"git@127.0.0.1", isolationPodman, "git@gitbay.org", "git@gitbay.org"},
		{"git@127.0.0.1", isolationPodman, "git@gitbay.test:2022", "git@gitbay.test:2022"},
		{"git@localhost", isolationPodman, "git@gitbay.org", "git@gitbay.org"},
		{"git@127.0.0.1", isolationPodman, "", "git@127.0.0.1"},
		{"git@gitbay.org", isolationPodman, "git@other.test", "git@gitbay.org"},
		{"gitbay.org", isolationPodman, "git@gitbay.org", "gitbay.org"},
	} {
		r := &runner{remote: tc.remote, isolation: tc.isolation}
		if got := r.buildSSH(tc.public); got != tc.want {
			t.Errorf("remote %s under %s, public %q: got %s want %s", tc.remote, tc.isolation, tc.public, got, tc.want)
		}
	}
}

// Only a runner that polls over loopback shares an address a build could
// connect from, so only its builds lose the host-loopback mapping (#260).
func TestBuildNetworkKeepsLoopbackRunnersBuildsOff(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   []string
	}{
		{"git@127.0.0.1", []string{"--network", "pasta:--no-map-gw"}},
		{"localhost", []string{"--network", "pasta:--no-map-gw"}},
		{"git@::1", []string{"--network", "pasta:--no-map-gw"}},
		{"git@gitbay.org", nil},
	} {
		r := &runner{remote: tc.remote, isolation: isolationPodman}
		if got := r.buildNetwork(); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("remote %s: %q, want %q", tc.remote, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/gitbay-runner -count=1`
Expected: build failure (`too many arguments in call to r.buildSSH`, `r.buildNetwork undefined`).

- [ ] **Step 3: Implement**

`job`, after `Trusted`:

```go
	// SSH is the instance's public ssh destination, for a build whose
	// runner polls over loopback (#260).
	SSH     string            `json:"ssh"`
```

Replace `buildSSH` (`main.go:465-486`) with:

```go
// loopbackRemote reports whether the runner polls the daemon on its own
// host over loopback.
func (r *runner) loopbackRemote() bool {
	_, host, ok := strings.Cut(r.remote, "@")
	if !ok {
		host = r.remote
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// buildSSH is the instance's ssh destination as a build reaches it. A
// runner polling over loopback keeps its podman builds off the host's
// loopback (buildNetwork), so they get the instance's public destination
// from the claim. Any other remote is a real host elsewhere and works as
// it is, and under -isolation none a build runs on the host itself.
func (r *runner) buildSSH(public string) string {
	if r.isolation == isolationPodman && r.loopbackRemote() && public != "" {
		return public
	}
	return r.remote
}

// buildNetwork is the podman network option for a build. pasta maps the
// container's gateway address to the host's loopback, and a build's
// connection through it arrives from 127.0.0.1 — the address a runner on
// the daemon's host polls from. The SSH auth limiter counts failures per
// source address, so a build sharing the runner's could throttle its
// polling (#260). --no-map-gw removes the mapping: the build reaches the
// host only at its public address, as any client on the internet does,
// and keeps its outbound access. The host's nftables table
// (deploy/gitbay-runner-egress.nft) then limits it to 22, 80 and 443
// there; it cannot tell a build from the runner by uid, so it leaves
// 127.0.0.1:22 open, and this flag is what keeps builds off it.
func (r *runner) buildNetwork() []string {
	if !r.loopbackRemote() {
		return nil
	}
	return []string{"--network", "pasta:--no-map-gw"}
}
```

In `run`, the `stepEnv` call becomes `env := stepEnv(j, home, r.buildSSH(j.SSH))`.

In `isolate.go`, after the `--env-file` append (line 153):

```go
	args = append(args, r.buildNetwork()...)
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go vet ./cmd/gitbay-runner && go test ./cmd/gitbay-runner -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gitbay-runner
git commit -S -m "runner: builds off the host's loopback when the runner polls over it

Ref #260"
```

### Task 3.3: the limiter's behaviour, by registration mode

This test records what the code does today (see "Finding for #260"
under Decisions), so it passes on its first run. It is the evidence the
issue's on-production throttling test was meant to give, without
touching production. If it fails, the limiter differs from what
Decisions and the wiki say: stop and correct that text, not the test.

**Files:**
- Test: `internal/sshd/sshd_test.go` (append; every import it needs is already in the file)

**Interfaces:**
- Consumes: `(*Server).authenticate`, `rateLimiter.seen` (package-internal).

- [ ] **Step 1: Write the test**

Append to `internal/sshd/sshd_test.go`:

```go
// authMeta is the connection metadata authenticate reads: only the
// remote address.
type authMeta struct {
	ssh.ConnMetadata
	addr net.Addr
}

func (m authMeta) RemoteAddr() net.Addr { return m.addr }

func authKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// authServer is a Server holding what authenticate uses: a store with a
// runner account's key, the registration mode, and a limiter of three
// failures a minute.
func authServer(t *testing.T, mode string) (*Server, ssh.PublicKey) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("ci", false)
	if err != nil {
		t.Fatal(err)
	}
	runner := authKey(t)
	if err := st.AddSSHKey(uid, ssh.FingerprintSHA256(runner), runner.Type(), runner.Marshal(), "runner", ""); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Registration.Mode = mode
	return &Server{cfg: cfg, st: st, authLimiter: newRateLimiter(3, time.Minute)}, runner
}

var (
	fromLoopback = authMeta{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}}
	fromPublic   = authMeta{addr: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 40000}}
)

// With registration closed an unknown key counts against its address.
// Below the limit a known key's success clears the count. At the limit
// authenticate refuses before it looks at the key, so the runner's own
// key from that address is refused too and its success never runs to
// clear anything, until the window passes. Another address is not
// affected. This is why a build must not share the runner's source
// address (#260).
func TestAuthLockoutHoldsAgainstTheRunnersKey(t *testing.T) {
	s, runner := authServer(t, "closed")
	stranger := authKey(t)
	failTimes := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := s.authenticate(fromLoopback, stranger); err == nil {
				t.Fatal("unknown key admitted with registration closed")
			}
		}
	}

	failTimes(2)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner below the limit: %v", err)
	}
	failTimes(2)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner after its success cleared the count: %v", err)
	}

	failTimes(3)
	for i := 0; i < 2; i++ {
		if _, err := s.authenticate(fromLoopback, runner); err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("attempt %d from a locked-out address: %v, want refused", i+1, err)
		}
	}
	if _, err := s.authenticate(fromPublic, runner); err != nil {
		t.Fatalf("another address was locked out too: %v", err)
	}

	s.authLimiter.seen["127.0.0.1"].start = time.Now().Add(-2 * time.Minute)
	if _, err := s.authenticate(fromLoopback, runner); err != nil {
		t.Fatalf("runner after the window passed: %v", err)
	}
}

// With registration open or by invite, an unknown key is admitted to run
// register and never counts, so no number of unknown-key attempts locks
// the runner's address out. gitbay.org runs open registration (#260).
func TestAuthUnknownKeyCountsOnlyWhenClosed(t *testing.T) {
	for _, mode := range []string{"open", "invite"} {
		s, runner := authServer(t, mode)
		for i := 0; i < 10; i++ {
			p, err := s.authenticate(fromLoopback, authKey(t))
			if err != nil || p.Extensions["anon-key"] == "" {
				t.Fatalf("%s: unknown key %d: %v %+v", mode, i+1, err, p)
			}
		}
		if _, err := s.authenticate(fromLoopback, runner); err != nil {
			t.Fatalf("%s: runner refused after unknown keys: %v", mode, err)
		}
	}
}
```

- [ ] **Step 2: Run it**

Run: `go vet ./internal/sshd && go test ./internal/sshd -run 'TestAuth' -count=1`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/sshd/sshd_test.go
git commit -S -m "sshd: test the auth limiter's lockout by registration mode

Ref #260"
```

### Task 3.4: host egress rule for the runner's uid

No Go code. The check script is the test: `make deploy-runner` runs it
after loading the rule and before restarting the runner, and runbook R3
runs the whole path against the scratch repository before merge.

**Files:**
- Create: `deploy/gitbay-runner-egress.nft`, `deploy/gitbay-runner-egress.service`, `deploy/runner-egress-check.sh`
- Modify: `deploy/gitbay-runner.override.conf` (a `[Unit]` section before `[Service]` at line 25)
- Modify: `deploy/runner-podman-setup.sh` (after `podman --version`, line 29)
- Modify: `Makefile:69-85` (`deploy-runner`)

**Interfaces:**
- Produces: nftables table `inet gitbay_runner`; unit
  `gitbay-runner-egress.service`, required by `gitbay-runner.service`;
  rule file at `/etc/gitbay-runner/egress.nft` on the runner host.

- [ ] **Step 1: The rule**

Create `deploy/gitbay-runner-egress.nft`:

```
#!/usr/sbin/nft -f
# Host egress for CI builds (#260). Loaded by gitbay-runner-egress.service,
# which gitbay-runner.service requires, so the runner does not start
# without it. `make deploy-runner` installs it as
# /etc/gitbay-runner/egress.nft.
#
# Under rootless podman with pasta, a build's connections are made by
# pasta on the host, from sockets owned by the runner's user, ci-runner.
# nftables sees them exactly as it sees the runner's own ssh, so this
# table cannot tell a build from its runner. It limits what that user
# reaches on this host, and the runner needs little: 127.0.0.1:22, to
# poll, clone and stream logs.
#
# Every packet to one of the host's own addresses, loopback or public,
# leaves through lo, so the output hook sees host-bound traffic as
# oifname "lo". Traffic to other hosts is not matched: builds keep
# outbound internet access, trusted or not (go mod download needs it).
#
# What ci-runner may reach on this host:
#   127.0.0.1:22      the forge over loopback, for the runner. Builds do
#                     not reach loopback at all: the runner starts them
#                     with pasta's gateway mapping off (--no-map-gw).
#   loopback :53      the host's resolver, which pasta forwards a
#                     build's DNS to when the host's nameserver is a
#                     loopback address.
#   public 22/80/443  the forge, as anyone on the internet reaches it.
# Everything else is rejected: the admin sshd on 2222 on every address,
# and any service bound to loopback. -isolation none builds run as the
# same user and get the same rule.
#
# The account name is resolved when the file is loaded. A restart of
# nftables.service (flush ruleset) removes this table; `systemctl
# reload gitbay-runner-egress` puts it back.

table inet gitbay_runner
delete table inet gitbay_runner

table inet gitbay_runner {
	chain output {
		type filter hook output priority filter; policy accept;
		oifname "lo" meta skuid "ci-runner" jump host
	}

	chain host {
		ip daddr 127.0.0.1 tcp dport 22 accept
		ip daddr 127.0.0.0/8 meta l4proto { tcp, udp } th dport 53 accept
		ip6 daddr ::1 meta l4proto { tcp, udp } th dport 53 accept
		ip daddr != 127.0.0.0/8 tcp dport { 22, 80, 443 } accept
		ip6 daddr != ::1 tcp dport { 22, 80, 443 } accept
		counter reject
	}
}
```

The first `table` line creates the table if it is missing so the
`delete` never fails; the file then replaces it in one transaction, so
a reload never leaves a moment without the rule. The uid match is in
the base chain's one rule rather than a `!=` accept, because a packet
with no socket (a kernel-sent reset) matches neither `==` nor `!=` on
`skuid` and would otherwise fall through to the reject.

- [ ] **Step 2: The unit**

Create `deploy/gitbay-runner-egress.service`:

```
# Loads the CI runner's host egress rule (#260,
# deploy/gitbay-runner-egress.nft). gitbay-runner.service requires this
# unit, so the runner starts only with the rule in force; stopping this
# unit removes the table and stops the runner with it.
#
# Ordered after nftables.service and ufw.service: either may rewrite the
# ruleset at boot, and nftables.service's default config starts with
# flush ruleset. A missing unit in After= is ignored.
#
# Reload re-reads the file and replaces the table in one transaction; it
# does not restart the runner, which a restart of this unit would
# (Requires= propagates restarts). `make deploy-runner` reloads.
[Unit]
Description=Host egress rule for CI builds
After=nftables.service ufw.service
Before=gitbay-runner.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/gitbay-runner/egress.nft
ExecReload=/usr/sbin/nft -f /etc/gitbay-runner/egress.nft
ExecStop=/usr/sbin/nft delete table inet gitbay_runner

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: The runner requires it**

In `deploy/gitbay-runner.override.conf`, insert before `[Service]`
(line 25):

```
[Unit]
# The host egress rule (#260, gitbay-runner-egress.nft) limits what this
# unit's user reaches on the host: 127.0.0.1:22 for the runner, the
# forge's public 22, 80 and 443 for builds, nothing else. Required, so
# the runner does not start without it: a table that failed to load must
# not mean builds reach the admin sshd.
Requires=gitbay-runner-egress.service
After=gitbay-runner-egress.service
```

- [ ] **Step 4: The check**

Create `deploy/runner-egress-check.sh`:

```sh
#!/bin/sh
# Check the CI runner's host egress rule (#260) as the runner's user:
# the forge over loopback on 22 must answer (the runner polls there),
# and the admin sshd on 2222 must not, on loopback or the public
# address. `make deploy-runner` runs this after loading the rule and
# before restarting the runner, and stops on a failure.
#
#   ssh -p 2222 root@bay1 'sh -s' < deploy/runner-egress-check.sh
set -eu

RUNNER_USER="${RUNNER_USER:-ci-runner}"
public=$(hostname -I | awk '{print $1}')

probe() {
    su -s /bin/bash "$RUNNER_USER" -c "timeout 5 bash -c 'exec 3<>/dev/tcp/$1/$2'" 2>/dev/null
}

nft list table inet gitbay_runner >/dev/null

for dest in 127.0.0.1:22 "$public:22"; do
    if ! probe "${dest%:*}" "${dest##*:}"; then
        echo "$RUNNER_USER cannot reach $dest: the egress rule would stop the runner" >&2
        exit 1
    fi
done
for dest in 127.0.0.1:2222 "$public:2222"; do
    if probe "${dest%:*}" "${dest##*:}"; then
        echo "$RUNNER_USER reaches $dest: the egress rule is not in force" >&2
        exit 1
    fi
done
echo "egress for $RUNNER_USER: 127.0.0.1:22 and $public:22 open, 2222 refused"
```

`hostname -I` lists the host's addresses, IPv4 first on bay1; the
first is the public one there.

- [ ] **Step 5: nftables on the host**

In `deploy/runner-podman-setup.sh`, after the podman install block
(after `podman --version`, line 29):

```sh
# nft loads the runner's host egress rule (#260,
# deploy/gitbay-runner-egress.nft), which `make deploy-runner` ships and
# the runner's unit requires. Without nft the runner does not start.
echo "==> installing nftables"
if ! command -v nft >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y nftables
fi
nft --version
```

- [ ] **Step 6: `make deploy-runner` ships, loads and checks it**

Replace `Makefile:69-85` with:

```make
deploy-runner: preflight
	@echo "==> building $(RUNNER_BIN)"
	$(CROSS) go build -trimpath -ldflags='$(LDFLAGS)' -o $(RUNNER_BIN) ./cmd/gitbay-runner
	@echo "==> pushing runner to $(HOST)"
	./deploy/copy.sh $(HOST) $(PORT) $(RUNNER_BIN) /usr/local/bin/gitbay-runner.new
	ssh -p $(PORT) root@$(HOST) 'mkdir -p /etc/systemd/system/gitbay-runner.service.d /etc/gitbay-runner'
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner.override.conf /etc/systemd/system/gitbay-runner.service.d/override.conf
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner-prune.service /etc/systemd/system/gitbay-runner-prune.service
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner-prune.timer /etc/systemd/system/gitbay-runner-prune.timer
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner-egress.nft /etc/gitbay-runner/egress.nft
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner-egress.service /etc/systemd/system/gitbay-runner-egress.service
	@echo "==> loading the egress rule"
	ssh -p $(PORT) root@$(HOST) 'set -eu; \
	  nft -c -f /etc/gitbay-runner/egress.nft; \
	  systemctl daemon-reload; \
	  systemctl enable gitbay-runner-egress.service; \
	  systemctl reload-or-restart gitbay-runner-egress.service'
	ssh -p $(PORT) root@$(HOST) 'sh -s' < deploy/runner-egress-check.sh
	ssh -p $(PORT) root@$(HOST) 'set -eu; \
	  chmod 755 /usr/local/bin/gitbay-runner.new; \
	  mv /usr/local/bin/gitbay-runner.new /usr/local/bin/gitbay-runner; \
	  systemctl enable --now gitbay-runner-prune.timer; \
	  systemctl restart gitbay-runner; \
	  systemctl --no-pager --lines=3 status gitbay-runner; \
	  systemctl --no-pager list-timers gitbay-runner-prune.timer'
```

`nft -c` checks the file without applying it, so a syntax error stops
the deploy with the old table and the old runner in place. On the first
deploy `reload-or-restart` starts the unit, and starting a required unit
does not restart the runner that requires it; later deploys reload it.
The check runs before the runner restarts, so a failure stops the
deploy with the old binary in place, but the new table is already
loaded and applies to the running runner too. If the check says the
rule blocks 127.0.0.1:22, remove the table with `ssh -p 2222
root@gitbay.org nft delete table inet gitbay_runner` (the unit stays
active, so the runner is not stopped with it), fix the rule, and deploy
again. Runbook R3 runs this path on the scratch runner before merge.

- [ ] **Step 7: Verify locally**

Run: `sh -n deploy/runner-egress-check.sh && sh -n deploy/runner-podman-setup.sh && make -n deploy-runner HOST=example.test`
Expected: no syntax errors; the dry run lists the two new copies, the
`nft -c` / `reload-or-restart` block, the check, then the runner block.

- [ ] **Step 8: Commit**

```bash
git add deploy/gitbay-runner-egress.nft deploy/gitbay-runner-egress.service deploy/runner-egress-check.sh deploy/gitbay-runner.override.conf deploy/runner-podman-setup.sh Makefile
git commit -S -m "runner host: builds reach only the forge's public ports on it

Ref #260"
```

### Task 3.5: egress policy in the wiki, and the MR

**Files:**
- Modify: `.gitbay/wiki/Threat-Model.org` ("The CI runner", after the *Images* bullet at line 169)
- Modify: `.gitbay/wiki/CI.org` (new section before "* The table", line 55)
- Modify: `.gitbay/wiki/Users.org:551-555` (the `GITBAY_SSH` sentence)
- Modify: `.gitbay/wiki/Admin.org` (after "It is idempotent.", line 668)
- Modify: `.gitbay/wiki/Architecture/07-CI-and-Supply-Chain.org:70` (Network row), `04-Trust-Boundaries.org:27` (TB7), `09-Controls.org:91`

- [ ] **Step 1: Threat-Model**

Add a bullet after *Images are provisioned…*:

```org
- *What a build can reach.* Outbound internet, trusted or not: a fork's
  merge request to a Go repository has to fetch its modules. On the
  runner's host, only the forge's public ports 22, 80 and 443, exactly
  as anyone on the internet reaches them. Two layers keep it there. A
  runner that polls the daemon over loopback starts its containers with
  pasta's gateway mapping off, so a build does not reach the host's
  loopback, and =GITBAY_SSH= names the public address; that keeps the
  runner's source address, =127.0.0.1=, one no build connects from. And
  an nftables table (=deploy/gitbay-runner-egress.nft=) rejects every
  connection the runner's user makes to the host's own addresses except
  =127.0.0.1:22=, DNS on loopback, and 22, 80 and 443 on the public
  address: the operator's sshd on 2222 and anything bound to loopback
  are closed to builds. Under rootless podman a build's connections are
  made by pasta as the runner's user, so the table cannot tell a build
  from its runner and leaves =127.0.0.1:22= open; the gateway mapping
  is what closes it to builds. The runner does not start without the
  table. The SSH auth limiter counts failures per source address and,
  once an address is over the limit, refuses every key from it until
  the window passes, the runner's included; with registration open or
  by invite an unknown key never counts (krz/gitbay#260). Under
  =-isolation none= a build runs on the host and shares its loopback;
  the table still applies, since it runs as the same user.
```

- [ ] **Step 2: CI.org**

Add before `* The table`:

```org
* What a build can reach

Builds have outbound internet access, trusted and untrusted alike. On
the runner's host they reach only the forge's public ports 22, 80 and
443: not the host's loopback, not the operator's sshd. The forge is
reached at its public address, the one in =GITBAY_SSH=. See the
Threat-Model page, "What a build can reach", for how and why
(krz/gitbay#260).
```

- [ ] **Step 3: Users.org**

Replace "(=git@gitbay.org= from a runner elsewhere; inside a container
on the server's own runner the host is at a private address the runner
fills in)" with "(=git@gitbay.org=, the instance's public address, from
a runner elsewhere and from a container on the server's own runner
alike; =git@host:port= on an instance whose ssh is not on 22, so use it
as =ssh://$GITBAY_SSH/owner/name.git= or =ssh ssh://$GITBAY_SSH …=,
which work in both forms)".

- [ ] **Step 4: Admin.org**

After "…verifies rootless podman actually runs as that user. It is
idempotent." add:

```org
It also installs nftables. =make deploy-runner= ships
=deploy/gitbay-runner-egress.nft= to =/etc/gitbay-runner/egress.nft=
with =gitbay-runner-egress.service=, which loads it and which the
runner's unit requires; it checks the file with =nft -c=, reloads the
unit, and runs =deploy/runner-egress-check.sh= as =ci-runner= before
restarting the runner: =127.0.0.1:22= and the public 22 must answer,
2222 must not. The table limits the runner's user to =127.0.0.1:22=,
DNS on loopback, and 22, 80 and 443 on the host's public address; the
Threat-Model page says why. A restart of =nftables.service= flushes it;
=systemctl reload gitbay-runner-egress= restores it.
```

- [ ] **Step 5: Architecture**

`07-CI-and-Supply-Chain.org:70` Network row:

```org
| Network                    | pasta; outbound open; a loopback runner's builds run with =--no-map-gw= (=main.go=); on the host only public 22/80/443 (=gitbay-runner-egress.nft=, #260) |
```

`04-Trust-Boundaries.org:27` TB7: replace "the network is open (#260)"
with "outbound is open; on the host only the forge's public ports
(#260)". `09-Controls.org:91`:

```org
| Build network egress restricted             | partial  | host: loopback closed, public 22/80/443 only (=gitbay-runner-egress.nft=); internet outbound open by decision (#260) |
```

The Known-Gaps row for #260 and its "What can a build reach…" question
stay until the runbook's R3 results are recorded on the CI page.

- [ ] **Step 6: Verify, commit, MR**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbay-runner ./internal/control ./internal/sshd -count=1`
Expected: PASS.

```bash
git add .gitbay/wiki
git commit -S -m "wiki: what a build can reach

Ref #260"
git push -u origin runner-source-address
gitbay mr create --source runner-source-address --target main --title "runner: keep builds off the runner's source address and the host's other ports"
```

Before merging, run the runbook's R3 on the scratch repository. Merge
with `--strategy ff`; delete the branch both places.

---

# Part 4: failed step and duration (branch `build-failure-report`, #266)

### Task 4.1: store the failed step

**Files:**
- Create: `internal/store/migrations/0065_build_failure.up.sql`, `0065_build_failure.down.sql`
- Modify: `internal/store/builds.go:12-33` (`Build`), `:67-78` (`buildSelect`, `scanBuild`), after `FinishBuild` (`:251-264`)
- Test: `internal/store/builds_test.go` (append; add `"errors"` to imports)

**Interfaces:**
- Produces:
  - `Build.FailedStep int`, `Build.FailedReason string`
  - `func (s *Store) SetBuildFailure(id int64, step int, reason string) error` — only on a running build; `ErrNotFound` otherwise.

- [ ] **Step 1: Write the failing test**

```go
// Where a failed build stopped is recorded while it runs, before the
// outcome, and a finished build is not rewritten (#266).
func TestSetBuildFailure(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, _ := s.CreateUser("cmc", true)
	repoID, _ := s.CreateRepo("user", uid, "app", "public")
	if _, err := s.CreateBuild(repoID, "unit", "abc", "main", `["true","false"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.ClaimBuild(nil, false)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := s.SetBuildFailure(b.ID, 2, "exit 1"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBuild(b.ID, "failure"); err != nil {
		t.Fatal(err)
	}
	got, err := s.BuildByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailedStep != 2 || got.FailedReason != "exit 1" {
		t.Fatalf("failed step %d reason %q", got.FailedStep, got.FailedReason)
	}
	if err := s.SetBuildFailure(b.ID, 1, "late"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rewrote a finished build: %v", err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run TestSetBuildFailure -count=1`
Expected: build failure (`s.SetBuildFailure undefined`).

- [ ] **Step 3: Migration**

`0065_build_failure.up.sql`:

```sql
-- Where a failed build stopped: the 1-based step, 0 when it stopped
-- before any step or did not fail, and the runner's one-line reason.
ALTER TABLE builds ADD COLUMN failed_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE builds ADD COLUMN failed_reason TEXT NOT NULL DEFAULT '';
```

`0065_build_failure.down.sql`:

```sql
ALTER TABLE builds DROP COLUMN failed_reason;
ALTER TABLE builds DROP COLUMN failed_step;
```

- [ ] **Step 4: Store**

In `Build`, after `Trusted`:

```go
	// FailedStep is the 1-based step a failed build stopped at, 0 when it
	// stopped before its first step or did not fail. FailedReason is the
	// runner's one line: "exit 1", "build timed out after 45m0s".
	FailedStep   int
	FailedReason string
```

`buildSelect` and `scanBuild`:

```go
const buildSelect = `
	SELECT id, repo_id, number, job, sha, ref, steps, image, tree, status, created_at, started_at, finished_at, log_closed_at, trusted,
	       failed_step, failed_reason
	FROM builds`

func scanBuild(row interface{ Scan(...any) error }) (Build, error) {
	var b Build
	var trusted int
	err := row.Scan(&b.ID, &b.RepoID, &b.Number, &b.Job, &b.SHA, &b.Ref, &b.Steps, &b.Image, &b.Tree,
		&b.Status, &b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.LogClosedAt, &trusted,
		&b.FailedStep, &b.FailedReason)
	b.Trusted = trusted != 0
	return b, err
}
```

After `FinishBuild`:

```go
// SetBuildFailure records where a running build failed. The runner
// reports it with the outcome; it is written first, so a reader woken
// by the finish sees both.
func (s *Store) SetBuildFailure(id int64, step int, reason string) error {
	res, err := s.DB.Exec(`UPDATE builds SET failed_step = ?, failed_reason = ?
		WHERE id = ? AND status = 'running'`, step, reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 5: Run the tests and see them pass**

Run: `go test ./internal/store -count=1`
Expected: PASS, `TestMigrateUpDown` included.

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -S -m "store: failed step and reason on a build

Ref #266"
```

### Task 4.2: `runner done --step --reason`

**Files:**
- Modify: `internal/control/build.go:102-106` (registration), `:648-713` (`runRunnerDone`)
- Test: `internal/control/runnernext_test.go` (append)

**Interfaces:**
- Consumes: `Store.SetBuildFailure`.
- Produces: `runner done <build-id> success|failure [--step <n>] [--reason <text>]`; `func failureReason(s string) string`.

- [ ] **Step 1: Write the failing tests**

```go
// runner done records the failed step and a one-line reason (#266).
func TestRunnerDoneRecordsFailedStep(t *testing.T) {
	st, repo, uid, root, baseSHA, _ := setupOrphanRepo(t)
	n, err := st.CreateBuild(repo.ID, "unit", baseSHA, "main", `["go build ./...","go test ./..."]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := st.ClaimBuild([]int64{repo.ID}, false)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	c, out := runnerCtx(st, uid, root)
	if code := runRunnerDone(c, []string{fmt.Sprint(b.ID), "failure", "--step", "2", "--reason", "exit 1\n"}); code != protocol.ExitOK {
		t.Fatalf("runner done: exit %d\n%s", code, out.String())
	}
	got, _ := st.BuildByNumber(repo.ID, n)
	if got.Status != "failure" || got.FailedStep != 2 || got.FailedReason != "exit 1" {
		t.Fatalf("status %s step %d reason %q", got.Status, got.FailedStep, got.FailedReason)
	}
}

// A report with no flags — an older runner — or with a step past the
// job's still finishes the build; the step is then recorded as 0.
func TestRunnerDoneToleratesMissingOrBadStep(t *testing.T) {
	st, repo, uid, root, baseSHA, _ := setupOrphanRepo(t)
	for _, extra := range [][]string{nil, {"--step", "9"}} {
		n, _ := st.CreateBuild(repo.ID, "unit", baseSHA, "main", `["true"]`, "", "", true)
		b, ok, err := st.ClaimBuild([]int64{repo.ID}, false)
		if err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		c, out := runnerCtx(st, uid, root)
		if code := runRunnerDone(c, append([]string{fmt.Sprint(b.ID), "failure"}, extra...)); code != protocol.ExitOK {
			t.Fatalf("runner done %v: exit %d\n%s", extra, code, out.String())
		}
		if got, _ := st.BuildByNumber(repo.ID, n); got.Status != "failure" || got.FailedStep != 0 {
			t.Fatalf("%v: status %s step %d", extra, got.Status, got.FailedStep)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run TestRunnerDone -count=1`
Expected: FAIL, the first with exit 2 (usage: four arguments where two
are accepted).

- [ ] **Step 3: Implement**

Registration:

```go
	register(Command{Path: []string{"runner", "done"},
		Summary: "finish a build",
		Usage:   "runner done <build-id> success|failure [--step <n>] [--reason <text>]",
		Flags: []Flag{
			{"--step", "<n>", "the 1-based step a failed build stopped at", ""},
			{"--reason", "<text>", "how it failed, one line", ""},
		},
		Examples: []string{"runner done 431 success", "runner done 431 failure --step 3 --reason 'exit 1'"},
		Run:      runRunnerDone})
```

Replace `runRunnerDone` with:

```go
func runRunnerDone(c *Ctx, args []string) int {
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	f, err := parseFlags(args, flagSpec{Values: []string{"--step", "--reason"}, MaxPos: 2,
		Usage: "runner done <build-id> success|failure [--step <n>] [--reason <text>]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 2 || (f.Pos[1] != "success" && f.Pos[1] != "failure") {
		return c.usage()
	}
	outcome := f.Pos[1]
	id, err := strconv.ParseInt(f.Pos[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", f.Pos[0])
	}
	b, err := c.Store.BuildByID(id)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	}
	if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository; a repository admin attaches it with repo runner add")
	}
	// Cancelled underneath the runner: its report is late, not wrong.
	// The row, the status and the log were settled by the cancel.
	if b.Status == "cancelled" {
		c.Store.RunnerDone(key.ID)
		return c.emit(map[string]any{"build": b.Number, "status": "cancelled"}, func(w io.Writer) {
			fmt.Fprintf(w, "build %d was cancelled\n", b.Number)
		})
	}
	if outcome == "failure" {
		// A step the job does not have is recorded as none rather than
		// refused: refusing would lose the outcome over a detail (#266).
		var steps []string
		json.Unmarshal([]byte(b.Steps), &steps)
		step, _ := strconv.Atoi(f.Value("--step"))
		if step < 0 || step > len(steps) {
			step = 0
		}
		if err := c.Store.SetBuildFailure(id, step, failureReason(f.Value("--reason"))); err != nil && !errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitFailure, "recording build %d's failure: %v", id, err)
		}
	}
	if err := c.Store.FinishBuild(id, outcome); err != nil {
		return c.fail(protocol.ExitFailure, "finishing build %d: %v", id, err)
	}
	c.Store.RunnerDone(key.ID)
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	url := fmt.Sprintf("%s/%s/builds/%d", c.Cfg.Server.SiteURL, repo.Path(), b.Number)
	desc := "build " + outcome
	if err := c.Store.SetCommitStatus(repo.ID, b.SHA, "ci/"+b.Job, outcome, desc, url, c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "build."+outcome,
		fmt.Sprintf(`{"number":%d,"job":%q,"sha":%q}`, b.Number, b.Job, b.SHA))
	// A red build mails the repo's notify targets with the log tail — a
	// failed scheduled job must not wait to be noticed.
	if outcome == "failure" {
		if targets, err := c.Store.RepoNotifyTargets(repo); err == nil {
			tail := ""
			if log, err := c.Store.BuildLog(id); err == nil && len(log) > 0 {
				if len(log) > 2000 {
					log = log[len(log)-2000:]
				}
				tail = string(log)
			}
			notify(c, targets, notice{repo: repo, kind: "build",
				subject: fmt.Sprintf("[%s] build %d failed: %s on %s", repo.Path(), b.Number, b.Job, b.Ref),
				action:  fmt.Sprintf("build %d failed: %s on %s", b.Number, b.Job, b.Ref),
				body:    fmt.Sprintf("job %s failed at %.10s.\n\n…%s\n\n%s\n", b.Job, b.SHA, tail, url),
				path:    fmt.Sprintf("%s/builds/%d", repo.Path(), b.Number)})
		}
	}
	return c.emit(map[string]any{"build": b.Number, "status": outcome}, func(w io.Writer) {
		fmt.Fprintf(w, "build %d %s\n", b.Number, outcome)
	})
}

// failureReason keeps a runner's reason to one line of at most 200
// bytes: it is shown on the build page and by build show.
func failureReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.ToValidUTF8(s, "")
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `go vet ./... && go test ./internal/control -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/build.go internal/control/runnernext_test.go
git commit -S -m "runner done: record the failed step and reason

Ref #266"
```

### Task 4.3: the runner names the failed step

**Files:**
- Modify: `cmd/gitbay-runner/main.go:245-277` (`step`), `:309-435` (`run`), `:363-398` (`runStep`)
- Modify: `cmd/gitbay-runner/isolate.go:79-197` (`runSteps`, `runStepsPodman`)
- Modify: `cmd/gitbay-runner/report.go:19-23` (`reportDone`), new `doneArgs`
- Test: `cmd/gitbay-runner/steps_test.go` (create), `cmd/gitbay-runner/report_test.go` (append)

**Interfaces:**
- Consumes: `runner done … --step <n> --reason <text>` from Task 4.2.
- Produces:
  - `type failure struct { Step int; Reason string }`
  - `func exitReason(err error) string`
  - `run(j job) *failure`, `runSteps(…) *failure`, `runStepsPodman(…) *failure` (nil is success)
  - `func (r *runner) reportDone(id int64, status string, f *failure) error`
  - `func doneArgs(id int64, status string, f *failure) []string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/gitbay-runner/steps_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The failing step is named by number in the log and in the outcome
// reported to the server (#266).
func TestRunStepsNamesTheFailedStep(t *testing.T) {
	r := &runner{isolation: isolationNone}
	run := func(cmd *exec.Cmd, _ time.Time) (bool, string) {
		if err := cmd.Run(); err != nil {
			return false, exitReason(err)
		}
		return true, ""
	}
	env := []string{"PATH=" + os.Getenv("PATH")}
	var log strings.Builder
	f := r.runSteps(job{Steps: []string{"true", "exit 3", "true"}}, t.TempDir(), env, &log, time.Now().Add(time.Minute), run)
	if f == nil || f.Step != 2 || f.Reason != "exit 3" {
		t.Fatalf("failure %+v, want step 2, exit 3", f)
	}
	if !strings.Contains(log.String(), "step 2/3 failed: exit 3\n") {
		t.Fatalf("log does not name the step:\n%s", log.String())
	}
	if f := r.runSteps(job{Steps: []string{"true"}}, t.TempDir(), env, &log, time.Now().Add(time.Minute), run); f != nil {
		t.Fatalf("a passing job failed: %+v", f)
	}
}
```

Append to `cmd/gitbay-runner/report_test.go` (add imports
`"strings"` and `"gitbay.org/gitbay/internal/protocol"`):

```go
// The reason survives the trip: ssh joins arguments with spaces and the
// server splits the line again with POSIX rules (#266).
func TestDoneArgsNameTheFailedStep(t *testing.T) {
	got := doneArgs(7, "failure", &failure{Step: 3, Reason: "can't: exit 1"})
	argv, err := protocol.Tokenize(strings.Join(got, " "))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"runner", "done", "7", "failure", "--step", "3", "--reason", "can't: exit 1"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("server reads %q, want %q", argv, want)
	}
	if got := doneArgs(7, "success", nil); strings.Join(got, " ") != "runner done 7 success" {
		t.Fatalf("success: %q", got)
	}
	if got := doneArgs(7, "failure", &failure{Reason: "git clone: exit 128"}); strings.Contains(strings.Join(got, " "), "--step") {
		t.Fatalf("a failure before any step sent a step: %q", got)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/gitbay-runner -count=1`
Expected: build failure (`undefined: exitReason`, `undefined: doneArgs`, `undefined: failure`).

- [ ] **Step 3: `failure` and `exitReason`**

In `main.go`, before `run`:

```go
// failure says where a build stopped: Step is the 1-based step that
// failed, 0 when the build stopped before its first step (the clone, the
// container), and Reason is one short line (#266).
type failure struct {
	Step   int
	Reason string
}

// exitReason is how a finished command's failure reads in a build's log
// and on the build: "exit 1" for a command that exited, the error
// otherwise (a signal, a start failure).
func exitReason(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return err.Error()
}
```

Add `"errors"` to `main.go`'s imports.

- [ ] **Step 4: `run` and `step`**

In `run`, the signature becomes `func (r *runner) run(j job) *failure`
with its comment "…Returns nil when every step succeeded, else where the
build stopped." Each early `return false` returns a failure instead:

```go
	home, doneHome, err := buildHome(r.workdir, j)
	if err != nil {
		log.Printf("build %d: build home: %v", j.ID, err)
		return &failure{Reason: "preparing the build home failed"}
	}
	defer doneHome()
```

```go
	pipe, err := logCmd.StdinPipe()
	if err != nil {
		log.Printf("build %d: log pipe: %v", j.ID, err)
		return &failure{Reason: "opening the log stream failed"}
	}
```

```go
	if err := logCmd.Start(); err != nil {
		log.Printf("build %d: log stream: %v", j.ID, err)
		return &failure{Reason: "opening the log stream failed"}
	}
```

In `runStep`, `return false, fmt.Sprintf("step failed: %v", err)`
becomes `return false, exitReason(err)`.

The clone loop's failure:

```go
		if ok, why := runStep(cmd, deadline); !ok {
			fmt.Fprintf(sink, "git %s: %s\n", args[0], why)
			return &failure{Reason: "git " + args[0] + ": " + why}
		}
```

`step()`, from `status := "failure"` to the report:

```go
	f := r.run(j)
	status := "success"
	if f != nil {
		status = "failure"
	}
	if err := r.reportDone(j.ID, status, f); err != nil {
		return true, err
	}
```

- [ ] **Step 5: `runSteps`, `runStepsPodman`**

In `isolate.go`, the `runSteps` comment ends "…Returns nil when every
step succeeded." and the function becomes:

```go
func (r *runner) runSteps(j job, dir string, env []string, sink io.Writer, deadline time.Time, runStep stepRunner) *failure {
	if r.isolation == isolationNone {
		for i, step := range j.Steps {
			fmt.Fprintf(sink, "$ %s\n", step)
			cmd := exec.Command(toolpath.Look("sh"), "-c", step)
			cmd.Dir, cmd.Env = dir, env
			cmd.Stdout, cmd.Stderr = sink, sink
			if ok, why := runStep(cmd, deadline); !ok {
				fmt.Fprintf(sink, "step %d/%d failed: %s\n", i+1, len(j.Steps), why)
				return &failure{Step: i + 1, Reason: why}
			}
		}
		return nil
	}
	return r.runStepsPodman(j, dir, env, sink, deadline, runStep)
}
```

`runStepsPodman` returns `*failure`. Its setup failures (env file,
cgroup, container start) keep their log lines and return
`&failure{Reason: "preparing the build environment failed"}`,
`&failure{Reason: "preparing the build cgroup failed"}` and
`&failure{Reason: "starting the build container failed"}`
respectively. The step loop:

```go
	for i, step := range j.Steps {
		fmt.Fprintf(sink, "$ %s\n", step)
		cmd := exec.Command(podman, append(r.podmanGlobal(), "exec", "--workdir", "/workspace", name, "sh", "-c", step)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.podmanHome()}
		intoCgroup(cmd, cgroupFD)
		cmd.Stdout, cmd.Stderr = sink, sink
		if ok, why := runStep(cmd, deadline); !ok {
			fmt.Fprintf(sink, "step %d/%d failed: %s\n", i+1, len(j.Steps), why)
			return &failure{Step: i + 1, Reason: why}
		}
	}
	return nil
```

`podman exec` exits with the step's own status, so `exit 1` is the
step's.

- [ ] **Step 6: `reportDone`, `doneArgs`**

In `report.go` (add `"strconv"` and `"strings"` to its imports):

```go
func (r *runner) reportDone(id int64, status string, f *failure) error {
	args := doneArgs(id, status, f)
	return reportWithRetry(func() (string, error) {
		return r.ssh(nil, args...)
	}, id, retryDelays)
}

// doneArgs is the runner done command for a build's outcome. The reason
// is single-quoted: ssh joins arguments with spaces, and the server
// splits the line again with POSIX rules.
func doneArgs(id int64, status string, f *failure) []string {
	args := []string{"runner", "done", fmt.Sprint(id), status}
	if f == nil {
		return args
	}
	if f.Step > 0 {
		args = append(args, "--step", strconv.Itoa(f.Step))
	}
	if f.Reason != "" {
		args = append(args, "--reason", "'"+strings.ReplaceAll(f.Reason, "'", `'\''`)+"'")
	}
	return args
}
```

(The comment above `reportDone` is unchanged.)

- [ ] **Step 7: Run the tests and see them pass**

Run: `go vet ./cmd/gitbay-runner && go test ./cmd/gitbay-runner -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/gitbay-runner
git commit -S -m "runner: name the failed step and report it

Ref #266"
```

### Task 4.4: `build show`, `build log --step/--tail`

**Files:**
- Create: `internal/control/buildlog.go`, `internal/control/buildlog_test.go`
- Modify: `internal/control/build.go:40-47` (registration), `:109-126` (`BuildOut`, `buildToOut`), `:221-238` (`runBuildShow`), `:240-258` (`runBuildLog`)

**Interfaces:**
- Consumes: `Build.FailedStep`, `Build.FailedReason`, `Build.Elapsed()`.
- Produces (used by Task 4.5):
  - `type LogSection struct { N int; Step string; Text string }`
  - `func SplitBuildLog(log string, steps []string) []LogSection`
  - `func FailedSection(sections []LogSection, status string, failedStep int) int`
  - `BuildOut.FailedStep int` (`failed_step`), `BuildOut.FailedReason string` (`failed_reason`), `BuildOut.DurationS int64` (`duration_s`), `BuildOut.Steps []string` (`steps`, on `build show` only)

- [ ] **Step 1: Write the failing tests**

Create `internal/control/buildlog_test.go`:

```go
package control

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestSplitBuildLog(t *testing.T) {
	log := "$ git clone ssh://x/a.git (abc)\n" +
		"$ go build ./...\n" +
		"built\n" +
		"$ go test ./...\n" +
		"--- FAIL: TestX\n" +
		"step 2/2 failed: exit 1\n"
	got := SplitBuildLog(log, []string{"go build ./...", "go test ./..."})
	want := []LogSection{
		{N: 0, Text: "$ git clone ssh://x/a.git (abc)\n"},
		{N: 1, Step: "go build ./...", Text: "built\n"},
		{N: 2, Step: "go test ./...", Text: "--- FAIL: TestX\nstep 2/2 failed: exit 1\n"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// A step's line inside other output, not at a line start, does not cut;
// a build that stopped before a step has no section for it; an empty
// setup is left out.
func TestSplitBuildLogStopsAtMissingStep(t *testing.T) {
	got := SplitBuildLog("$ make\nrunning: $ make test\nerror\n", []string{"make", "make test"})
	want := []LogSection{{N: 1, Step: "make", Text: "running: $ make test\nerror\n"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTailLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 5, "a\nb\nc\n"},
		{"a\nb", 1, "b"},
	} {
		if got := string(tailLines([]byte(tc.in), tc.n)); got != tc.want {
			t.Errorf("tailLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// failedBuild is a finished failure whose second of two steps failed,
// having run 10m56s.
func failedBuild(t *testing.T) (*store.Store, store.Repo, int64, int64) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	n, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["go build ./...","go test ./..."]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := st.ClaimBuild([]int64{repo.ID}, false)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	st.AppendBuildLog(b.ID, []byte("$ git clone x (abc)\n$ go build ./...\nok\n$ go test ./...\none\n--- FAIL: TestX\nstep 2/2 failed: exit 1\n"))
	if err := st.SetBuildFailure(b.ID, 2, "exit 1"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishBuild(b.ID, "failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE builds SET started_at = '2026-09-27T10:00:00Z', finished_at = '2026-09-27T10:10:56Z' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	return st, repo, uid, n
}

func TestBuildLogStepAndTail(t *testing.T) {
	st, repo, uid, n := failedBuild(t)
	run := func(args ...string) (string, int) {
		t.Helper()
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
		code := Dispatch(c, append([]string{"build", "log", repo.Path(), fmt.Sprint(n)}, args...))
		return c.Stdout.(*bytes.Buffer).String() + errOut.String(), code
	}
	if out, _ := run("--step", "1"); out != "ok\n" {
		t.Errorf("--step 1: %q", out)
	}
	if out, _ := run("--step", "failed", "--tail", "2"); out != "--- FAIL: TestX\nstep 2/2 failed: exit 1\n" {
		t.Errorf("--step failed --tail 2: %q", out)
	}
	if out, _ := run("--tail", "1"); out != "step 2/2 failed: exit 1\n" {
		t.Errorf("--tail 1: %q", out)
	}
	if _, code := run("--step", "3"); code != protocol.ExitUsage {
		t.Errorf("--step past the job: exit %d", code)
	}
	if _, code := run("--follow", "--tail", "1"); code != protocol.ExitUsage {
		t.Errorf("--follow with --tail: exit %d", code)
	}
}

func TestBuildShowNamesFailedStepAndDuration(t *testing.T) {
	st, repo, uid, n := failedBuild(t)
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"build", "show", repo.Path(), fmt.Sprint(n)}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()
	for _, re := range []string{`failed step\s+2/2 go test \./\.\.\. \(exit 1\)`, `duration\s+10m56s`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("build show missing %s:\n%s", re, out)
		}
	}
	c, _ = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.JSON = true
	Dispatch(c, []string{"build", "show", repo.Path(), fmt.Sprint(n)})
	for _, want := range []string{`"failed_step":2`, `"failed_reason":"exit 1"`, `"duration_s":656`, `"steps":["go build ./...","go test ./..."]`} {
		if !strings.Contains(c.Stdout.(*bytes.Buffer).String(), want) {
			t.Errorf("build show --json missing %s", want)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run 'TestSplitBuildLog|TestTailLines|TestBuildLogStep|TestBuildShowNames' -count=1`
Expected: build failure (`undefined: SplitBuildLog`).

- [ ] **Step 3: `buildlog.go`**

```go
package control

import "strings"

// LogSection is one part of a build log: the setup before the first
// step (N 0), or one step and its output.
type LogSection struct {
	N    int    // 0 for the setup, else the 1-based step
	Step string // the step's command; "" for the setup
	Text string
}

// SplitBuildLog cuts a log at the "$ <step>" line the runner writes
// before each step, matching the build's steps in order and only at a
// line start. Output before the first step is the setup section, left
// out when empty. A step with no line in the log — the build stopped
// before it — has no section, and neither has any step after it.
func SplitBuildLog(log string, steps []string) []LogSection {
	var out []LogSection
	cur := LogSection{}
	start := 0
	for i, step := range steps {
		marker := "$ " + step + "\n"
		at := findLine(log, marker, start)
		if at < 0 {
			break
		}
		cur.Text = log[start:at]
		if cur.N > 0 || cur.Text != "" {
			out = append(out, cur)
		}
		cur = LogSection{N: i + 1, Step: step}
		start = at + len(marker)
	}
	cur.Text = log[start:]
	if cur.N > 0 || cur.Text != "" {
		out = append(out, cur)
	}
	return out
}

// findLine is the index of line in log at or after from where it starts
// a line, or -1.
func findLine(log, line string, from int) int {
	for i := from; i <= len(log)-len(line); {
		j := strings.Index(log[i:], line)
		if j < 0 {
			return -1
		}
		at := i + j
		if at == 0 || log[at-1] == '\n' {
			return at
		}
		i = at + 1
	}
	return -1
}

// FailedSection is the index of the section a failed build stopped in:
// the step the runner named, or the last section when it named none (an
// older runner, or a failure the runner could not tie to a step). -1
// when the build did not fail or its log is empty.
func FailedSection(sections []LogSection, status string, failedStep int) int {
	if status != "failure" || len(sections) == 0 {
		return -1
	}
	for i, s := range sections {
		if failedStep > 0 && s.N == failedStep {
			return i
		}
	}
	return len(sections) - 1
}

// tailLines is the last n lines of b; a final newline ends the last line
// rather than starting another.
func tailLines(b []byte, n int) []byte {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			n--
			if n == 0 {
				return b[i+1:]
			}
		}
	}
	return b
}
```

- [ ] **Step 4: `BuildOut`, `build show`**

`BuildOut`, after `Subject`:

```go
	// FailedStep is the 1-based step a failed build stopped at, 0 when
	// none; FailedReason says how ("exit 1") (#266).
	FailedStep   int    `json:"failed_step,omitempty"`
	FailedReason string `json:"failed_reason,omitempty"`
	// DurationS is how long the build ran, once it has a start and a
	// finish.
	DurationS int64 `json:"duration_s,omitempty"`
	// Steps are the job's commands; build show only.
	Steps []string `json:"steps,omitempty"`
```

`buildToOut`:

```go
func buildToOut(b store.Build) BuildOut {
	return BuildOut{Number: b.Number, Job: b.Job, Status: b.Status, SHA: b.SHA,
		Ref: b.Ref, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
		FailedStep: b.FailedStep, FailedReason: b.FailedReason,
		DurationS: int64(b.Elapsed() / time.Second)}
}
```

`runBuildShow`:

```go
func runBuildShow(c *Ctx, args []string) int {
	repo, b, code := buildRef(c, args)
	if code >= 0 {
		return code
	}
	d := buildToOut(b)
	json.Unmarshal([]byte(b.Steps), &d.Steps)
	return c.emit(d, func(w io.Writer) {
		failedStep, failed := "", ""
		if d.FailedStep > 0 && d.FailedStep <= len(d.Steps) {
			step, _, _ := strings.Cut(d.Steps[d.FailedStep-1], "\n")
			failedStep = fmt.Sprintf("%d/%d %s", d.FailedStep, len(d.Steps), step)
			if d.FailedReason != "" {
				failedStep += " (" + d.FailedReason + ")"
			}
		} else {
			failed = d.FailedReason
		}
		duration := ""
		if d.DurationS > 0 {
			duration = (time.Duration(d.DurationS) * time.Second).String()
		}
		v := c.view(w)
		v.title(fmt.Sprintf("#%d", d.Number), d.Job, d.Status)
		v.fields(
			"sha", fmt.Sprintf("%.10s", d.SHA),
			"ref", d.Ref,
			"queued", c.when(d.CreatedAt),
			"finished", c.when(d.FinishedAt),
			"duration", duration,
			"failed step", failedStep,
			"failed", failed,
			"url", c.siteURL(repo.Path(), "builds", strconv.FormatInt(d.Number, 10)),
		)
	})
}
```

- [ ] **Step 5: `build log`**

Registration:

```go
	register(Command{Path: []string{"build", "log"},
		Summary: "print a build's log, or follow it until the build ends",
		Usage:   "build log <owner/name> <n> [--follow] [--step <step>|failed] [--tail <lines>]",
		Flags: []Flag{
			{"--follow", "", "stream the log until the build ends", ""},
			{"--step", "<step>|failed", "only one step's output: 0 for the setup, a step number, or the one that failed", ""},
			{"--tail", "<lines>", "only the last lines", ""},
		},
		Examples: []string{"build log krz/gitbay 431 --follow", "build log krz/gitbay 431 --step failed --tail 40"},
		ReadOnly: true, Run: runBuildLog})
```

`runBuildLog`:

```go
func runBuildLog(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Bools: []string{"--follow"}, Values: []string{"--step", "--tail"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	repo, b, code := buildRef(c, f.Pos)
	if code >= 0 {
		return code
	}
	if f.Has("--follow") {
		if f.Has("--step") || f.Has("--tail") {
			return c.fail(protocol.ExitUsage, "--step and --tail read the stored log; drop --follow")
		}
		return followBuildLog(c, repo, b)
	}
	tail := 0
	if f.Has("--tail") {
		if tail, err = strconv.Atoi(f.Value("--tail")); err != nil || tail < 1 {
			return c.fail(protocol.ExitUsage, "--tail takes a number of lines, 1 or more")
		}
	}
	log, err := c.Store.BuildLog(b.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if f.Has("--step") {
		var steps []string
		json.Unmarshal([]byte(b.Steps), &steps)
		sections := SplitBuildLog(string(log), steps)
		at := -1
		if want := f.Value("--step"); want == "failed" {
			if at = FailedSection(sections, b.Status, b.FailedStep); at < 0 {
				return c.fail(protocol.ExitNotFound, "build %d did not fail", b.Number)
			}
		} else {
			n, err := strconv.Atoi(want)
			if err != nil || n < 0 || n > len(steps) {
				return c.fail(protocol.ExitUsage, "--step takes 0 (the setup) to %d, or failed", len(steps))
			}
			for i, s := range sections {
				if s.N == n {
					at = i
				}
			}
			if at < 0 {
				return c.fail(protocol.ExitNotFound, "build %d has no output for step %d", b.Number, n)
			}
		}
		log = []byte(sections[at].Text)
	}
	if tail > 0 {
		log = tailLines(log, tail)
	}
	c.Stdout.Write(log)
	return protocol.ExitOK
}
```

- [ ] **Step 6: Run the tests and see them pass**

Run: `go vet ./... && go test ./internal/control -count=1 && go test ./cmd/gitbay -run TestSummariesAreCurrent -count=1`
Expected: PASS (the `build log` summary is unchanged; no regeneration
needed).

- [ ] **Step 7: Commit**

```bash
git add internal/control
git commit -S -m "build show: failed step and duration; build log --step, --tail

Ref #266"
```

### Task 4.5: the build page folds by step

**Files:**
- Modify: `internal/httpd/builds.go:1-18` (imports: add `"time"`), `:287-322` (`build`, `buildView`)
- Modify: `internal/web/templates/build.html:14-17`
- Modify: `internal/web/static/style.css:1116`
- Test: `internal/httpd/buildpages_test.go` (append)

**Interfaces:**
- Consumes: `control.SplitBuildLog`, `control.FailedSection`, `control.LogSection`, `BuildOut` fields from Task 4.4.
- Produces: `buildView.Steps []logStep`, `buildView.Failed bool`, `buildView.Duration string`; `func logSteps(log string, b control.BuildOut) ([]logStep, bool)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpd/buildpages_test.go`:

```go
// A failed build's page folds its log by step, opens the step that
// failed and links to it; no JavaScript (#266).
func TestBuildPageFoldsStepsAndOpensFailure(t *testing.T) {
	b := control.BuildOut{Number: 61, Job: "test", Status: "failure",
		SHA: "ff6271a9d4570cd46f169091637a9d2e40ad5c2b", Ref: "main",
		CreatedAt: "2026-08-28T04:42:54Z", FinishedAt: "2026-08-28T04:53:50Z", DurationS: 656,
		Steps: []string{"go build ./...", "go test ./..."}, FailedStep: 2, FailedReason: "exit 1"}
	log := "$ git clone x (ff6271a9d4)\n$ go build ./...\n$ go test ./...\n--- FAIL: TestCLI\nstep 2/2 failed: exit 1\n"
	v := buildView{repoPage: testRepoPage(), Build: b, Log: log, Duration: "10m56s"}
	v.Steps, v.Failed = logSteps(log, b)
	var sb strings.Builder
	if err := web.Render(&sb, "build.html", v); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		`<details class="difffold buildstep" id="failed" open>`,
		"step 2/2", "<code>go test ./...</code>", `href="#failed"`, "Jump to failure",
		"ran 10m56s", "--- FAIL: TestCLI",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("build.html missing %q", want)
		}
	}
	if n := strings.Count(out, `class="difffold buildstep"`); n != 3 {
		t.Errorf("%d step folds, want 3 (setup and two steps)", n)
	}
	if n := strings.Count(out, `id="failed"`); n != 1 {
		t.Errorf("%d failed anchors, want 1", n)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/httpd -run TestBuildPageFoldsStepsAndOpensFailure -count=1`
Expected: build failure (`undefined: logSteps`).

- [ ] **Step 3: Handler**

Replace `buildView` and add `logStep` and `logSteps`:

```go
type buildView struct {
	repoPage
	Build control.BuildOut
	Log   string
	// Steps is the finished log cut at its steps, nil when there is no
	// step to cut at; Failed says whether one of them is marked failed.
	Steps    []logStep
	Failed   bool
	Duration string
	Live     bool
	CanWrite bool
	Notice   string
}

type logStep struct {
	control.LogSection
	Failed bool
}

// logSteps cuts a finished build's log at its steps and marks the one it
// failed at. Nil when no step's line is in the log — a build that
// stopped in the clone — which renders as one block.
func logSteps(log string, b control.BuildOut) ([]logStep, bool) {
	sections := control.SplitBuildLog(log, b.Steps)
	stepped := false
	for _, s := range sections {
		if s.N > 0 {
			stepped = true
		}
	}
	if !stepped {
		return nil, false
	}
	failed := control.FailedSection(sections, b.Status, b.FailedStep)
	out := make([]logStep, len(sections))
	for i, s := range sections {
		out[i] = logStep{LogSection: s, Failed: i == failed}
	}
	return out, failed >= 0
}
```

In `build`, replace the last two lines (`v.Log, _, _ = …` and
`s.render(…)`) with:

```go
	v.Log, _, _ = s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})
	v.Steps, v.Failed = logSteps(v.Log, b)
	if b.DurationS > 0 {
		v.Duration = (time.Duration(b.DurationS) * time.Second).String()
	}
	s.render(w, "build.html", v)
```

Add `"time"` to the imports.

- [ ] **Step 4: Template**

Replace `build.html:14-17` with:

```html
<p class="meta">{{.Build.Job}} on {{.Build.Ref}} · <code><a href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/commit/{{.Build.SHA}}">{{printf "%.10s" .Build.SHA}}</a></code> · queued {{when .Build.CreatedAt}}{{if .Build.FinishedAt}} · finished {{when .Build.FinishedAt}}{{with .Duration}} · ran {{.}}{{end}}{{end}}{{if .Failed}} · <a href="#failed">Jump to failure</a>{{end}}</p>
{{if .Live}}<p class="meta">Live: the log streams here until the build ends. If it stops without a “build finished” line, reload to pick it up again. <a href="?follow=0">Show it without updates</a></p>
<pre class="code buildlog" tabindex="0">{{.Log}}</pre>
{{else if .Steps}}{{$total := len .Build.Steps}}{{range .Steps}}
<details class="difffold buildstep"{{if .Failed}} id="failed" open{{end}}>
  <summary>{{if .N}}<span>step {{.N}}/{{$total}}</span> <code>{{.Step}}</code>{{else}}<span>setup</span>{{end}}{{if .Failed}} <span class="chip check-failure">failed</span>{{end}}</summary>
  <pre class="code buildlog" tabindex="0">{{.Text}}</pre>
</details>{{end}}
{{else if .Log}}<pre class="code buildlog" tabindex="0">{{.Log}}</pre>{{else}}<p class="empty-note">no log yet</p>{{end}}
```

The live branch keeps its single `<pre>` directly after the marker, which
`streamBuild` requires (`builds.go:340`).

- [ ] **Step 5: CSS**

Replace `style.css:1116` with:

```css
pre.buildlog { max-height: 40rem; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
details.buildstep pre.buildlog { margin: 0; border: 0; border-radius: 0; }
details.buildstep summary code { overflow-wrap: anywhere; }
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/httpd ./internal/web -count=1`
Expected: PASS (`TestPreBlocksAreFocusable` sees the new `<pre>` with
`tabindex="0"`; no new template, so `TestMainWidthClass` is unchanged).

- [ ] **Step 7: Commit**

```bash
git add internal/httpd internal/web
git commit -S -m "web: build log folded by step, failed step open

Ref #266"
```

### Task 4.6: e2e, wiki, and the MR

**Files:**
- Modify: `e2e/ci_test.go:145-149` (`TestCI`)
- Modify: `.gitbay/wiki/CI.org` (after the `build log --follow` paragraph), `.gitbay/wiki/Users.org:542-547`, `.gitbay/wiki/Parity.org:211-213`, `.gitbay/wiki/Architecture/07-CI-and-Supply-Chain.org` (lifecycle step 5)

- [ ] **Step 1: e2e**

Replace `e2e/ci_test.go:145-149` with:

```go
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "log", "alice/app", brokenN)
	if !strings.Contains(out, "step 1/1 failed: exit 1") {
		t.Fatalf("broken log:\n%s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "show", "alice/app", brokenN)
	if !strings.Contains(out, "1/1 false (exit 1)") {
		t.Fatalf("build show does not name the failed step:\n%s", out)
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "log", "alice/app", brokenN, "--step", "failed"); strings.Contains(out, "git clone") || !strings.Contains(out, "exit 1") {
		t.Fatalf("build log --step failed:\n%s", out)
	}
	if _, body := inst.get(t, "/alice/app/builds/"+brokenN); !strings.Contains(body, `id="failed" open`) {
		t.Fatalf("build page does not open the failed step:\n%s", body)
	}
```

Before editing, check the `broken` job's step in `TestCI`'s `ci.yml`
(line ~100) is `"false"`; the expected `1/1 false` follows from it.

Run: `go test ./e2e -run 'TestCI$' -count=1`
Expected: PASS.

- [ ] **Step 2: Wiki**

CI.org, after the `build log --follow` paragraph:

```org
A failed build names the step it stopped at: the log's last line reads
=step 3/3 failed: exit 1=, and =build show= prints =failed step= (=3/3
go test ./... (exit 1)=) and =duration=. =build log <owner/name> <n>
--step failed= prints only that step's output, =--step 2= another one
(=0= is the clone before the first step), and =--tail 40= the last forty
lines of whichever was chosen; neither combines with =--follow=. The
build page folds the finished log into one section per step, opens the
failed one and links to it from the top as "Jump to failure". Builds
from before this reported no step; their last section is taken as the
failed one.
```

Users.org, after "…=build list= takes =--ref=, =--status= and =--job=
to narrow the listing, combinable;" sentence group, add: "=build show=
names a failed build's step and how long it ran, and =build log= takes
=--step <n>|failed= and =--tail <lines>=."

Parity.org, after `build log follow (until it ends)`:

```org
| build failed step, duration | yes | yes | no  |
| build log one step          | yes | yes | no  |
| build log tail              | yes | no  | no  |
```

`07-CI-and-Supply-Chain.org`, lifecycle step 5, replace
"=runner done <id> success|failure= sets the status," with "=runner
done <id> success|failure [--step <n>] [--reason <text>]= records where
a failed build stopped, sets the status,".

- [ ] **Step 3: Verify, commit, MR**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbay-runner ./cmd/gitbay ./internal/control ./internal/store ./internal/httpd ./internal/web -count=1`
Expected: PASS.

```bash
git add e2e/ci_test.go .gitbay/wiki
git commit -S -m "wiki: failed step, build log --step and --tail

Closes #266"
git push -u origin build-failure-report
gitbay mr create --source build-failure-report --target main --title "builds: name the failed step and duration; jump to failure"
```

Deploy `gitbayd` (schema 64→65 on the first start, or whatever the
number is after renumbering), then validate per runbook R4, then merge
with `--strategy ff` and delete the branch both places.

---

# Open questions

1. **pasta on bay1.** The plan relies on `--network pasta:--no-map-gw`
   removing the host-loopback path and on a pasta container reaching the
   host's public address. The code comment at `main.go:465-470` says
   pasta exposes the host at `169.254.1.2` as its `--map-host-loopback`
   default; newer podman instead passes `--map-guest-addr 169.254.1.2`,
   which maps to the host's public address. Which one bay1's podman
   does is not in the repository. Runbook R3 step 0 measures it before
   Part 3 deploys; if `--no-map-gw` is refused or leaves `127.0.0.1`
   reachable, stop and revisit Part 3 before merging. The nftables
   table does not cover this path: a build's connection to 127.0.0.1:22
   through pasta is ci-runner's, like the runner's own poll.

---

# Operator runbook (cmc)

Everything here runs from the laptop against bay1. One forge write per
Bash call; after `make deploy` the CLI's control master is gone, so do
not poll with several ssh calls a tick. Operator ssh is
`ssh -p 2222 root@gitbay.org`.

### R1. Scratch repository and scoped runner (once, before Part 1's runner deploy)

1. Create the scratch repository and its fork, and attach the bay1
   runner key to the scratch repository only:

   ```sh
   gitbay repo create cmc/ci-scratch --private
   gitbay repo fork cmc/ci-scratch --name ci-scratch-fork
   ssh -p 2222 root@gitbay.org cat /var/lib/gitbay-runner/.ssh/id_ed25519.pub > /tmp/ci-runner.pub
   gitbay repo runner add cmc/ci-scratch < /tmp/ci-runner.pub
   ```

2. Scope the service to it with a second drop-in that sorts after
   `override.conf`. Copy the current `ExecStart` from
   `deploy/gitbay-runner.override.conf` and add
   `-repos cmc/ci-scratch`:

   ```sh
   ssh -p 2222 root@gitbay.org 'cat > /etc/systemd/system/gitbay-runner.service.d/zz-scratch.conf' <<'EOF'
   [Service]
   ExecStart=
   ExecStart=/usr/local/bin/gitbay-runner -remote git@127.0.0.1 -workdir /var/lib/gitbay-runner/work -poll 5s -timeout 45m -isolation podman -image localhost/gitbay-ci:2 -cpus 3 -memory 6g -untrusted -repos cmc/ci-scratch
   EOF
   ```

   `make deploy-runner` reloads and restarts the unit, so the scoped
   `ExecStart` is live for each validation below. While it is in place
   builds of real repositories queue and wait.

3. After each validation: remove the drop-in and restart.

   ```sh
   ssh -p 2222 root@gitbay.org 'rm /etc/systemd/system/gitbay-runner.service.d/zz-scratch.conf && systemctl daemon-reload && systemctl restart gitbay-runner'
   ```

### R2. Part 1 (#255): validate, then discard the old homes

1. `make deploy` from the Part 1 branch; `curl -s https://gitbay.org/healthz`
   names its commit. Install the R1 drop-in, then `make deploy-runner`.
2. On `cmc/ci-scratch` `main`, push a `.gitbay/ci.yml`:

   ```yaml
   jobs:
     home:
       steps:
         - echo "HOME=$HOME"
         - test ! -e "$HOME/poison" || { echo "POISONED"; exit 1; }
         - touch "$HOME/trusted-marker"
   ```

   The build passes; its log shows `HOME=/var/lib/gitbay-runner/work/trusted-home/cmc/ci-scratch`.
3. In `cmc/ci-scratch-fork`, change the step list to
   `echo "HOME=$HOME"`, `ls -a "$HOME"`, `touch "$HOME/poison"`, push a
   branch and open an MR into `cmc/ci-scratch`. The untrusted build's
   log shows `HOME=/var/lib/gitbay-runner/work/build-<id>-home` and an
   empty listing (no `trusted-marker`).
4. On bay1: `ls /var/lib/gitbay-runner/work` shows no `build-<id>-home`
   left behind.
5. `gitbay build trigger cmc/ci-scratch home`: passes (no `POISONED`).
6. Remove the R1 drop-in (R1 step 3). Merge Part 1.
7. Discard the homes that trusted and untrusted builds shared:

   ```sh
   ssh -p 2222 root@gitbay.org 'chmod -R u+w /var/lib/gitbay-runner/work/home && rm -rf /var/lib/gitbay-runner/work/home'
   ```

   The laptop runner (`~/Library/Caches/gitbay-runner/home` or its
   configured workdir) gets the same once its brew bottle carries Part 1.
8. Record: the release's CHANGELOG upgrade note says to deploy gitbayd
   before runners, and that `<workdir>/home` can be deleted after the
   runner upgrade. Nothing further in the wiki; Part 1's MR updated it.

### R3. Part 3 (#260): pasta check, egress rule, measurement from a build

No throttling test runs on production; Task 3.3's unit test covers the
limiter. What is measured here is what a build sees.

0. Before merging Part 3, on bay1: re-run the host setup (idempotent;
   it now installs nftables), then check pasta as the runner user:

   ```sh
   ssh -p 2222 root@gitbay.org 'sh -s' < deploy/runner-podman-setup.sh
   ssh -p 2222 root@gitbay.org "podman --version; pasta --version | head -1; nft --version; hostname -I"
   ssh -p 2222 root@gitbay.org "su - ci-runner -s /bin/sh -c 'podman --cgroup-manager=cgroupfs run --rm --pull=never --network pasta:--no-map-gw --entrypoint sh localhost/gitbay-ci:2 -c \"getent hosts proxy.golang.org; timeout 5 bash -c \\\"exec 3<>/dev/tcp/gitbay.org/22\\\" && echo public-ok; cat /proc/net/route\"'"
   ```

   Expected: `proxy.golang.org` resolves, `public-ok` prints, and
   `hostname -I` lists the public IPv4 address first (the check script
   takes the first). If podman rejects the option, stop (open question
   1).
1. `make deploy` from the Part 3 branch, the R1 drop-in, then
   `make deploy-runner`. Its output shows `==> loading the egress rule`
   and then `egress for ci-runner: 127.0.0.1:22 and <public>:22 open,
   2222 refused` before the runner restarts. If the check fails, follow
   Task 3.4 Step 6 (remove the table, fix, deploy again) before
   anything else: the running runner is under the new table.
2. On `cmc/ci-scratch` `main`, a probe job (keep each step under 4096
   bytes):

   ```yaml
   jobs:
     probe:
       steps:
         - echo "GITBAY_SSH=$GITBAY_SSH"
         - |
           host=${GITBAY_SSH#*@}; host=${host%:*}
           for t in 127.0.0.1:22 127.0.0.1:2222 169.254.1.2:22 $host:22 $host:80 $host:443 $host:2222 proxy.golang.org:443; do
             timeout 5 bash -c "exec 3<>/dev/tcp/${t%:*}/${t##*:}" 2>/dev/null && echo "open $t" || echo "closed $t"
           done
         - bash -c 'exec 3<>/dev/tcp/${GITBAY_SSH#*@}/22; sleep 90'
   ```

3. While the last step holds its connection open, on bay1:

   ```sh
   ssh -p 2222 root@gitbay.org "ss -tn state established '( sport = :22 )'"
   ```

   Note the peer address of the build's connection and of the runner's
   `runner log` session. Expected: the runner's is `127.0.0.1`, the
   build's is the host's public address, never `127.0.0.1`.
4. Expected build log: `GITBAY_SSH=git@gitbay.org`; `closed
   127.0.0.1:22`, `closed 127.0.0.1:2222`; `169.254.1.2:22` as
   measured (open only if podman maps that address to the public one);
   `open gitbay.org:22`, `:80`, `:443`; `closed gitbay.org:2222`;
   `open proxy.golang.org:443`.
5. The runner kept polling: `gitbay admin runners` shows a recent poll
   for the bay1 key, and the probe build finished with its log.
6. Remove the R1 drop-in. Trigger one real trusted job that talks back
   (`gitbay build trigger krz/orgo <its release or pages job>` only if
   one is due; otherwise wait for the next hutch/orgo scheduled job) and
   check it reached `git@gitbay.org`.
7. Record the result, one commit on a branch `wiki-260-results` with
   `Closes #260`: in CI.org under "What a build can reach", a dated
   paragraph with the podman, pasta and nft versions, the source
   address the forge saw for a build and for the runner (step 3), and
   the reachability list from step 4. In
   `Architecture/10-Known-Gaps.org`, remove the `#260` row and answer
   "What can a build reach on the host's network?" with the date and a
   pointer to the CI page. `09-Controls` stays `partial` (outbound is
   open by decision). MR, `--strategy ff`, delete the branch.

### R4. Part 4 (#266): validate the failure report

1. `make deploy` from the Part 4 branch (migration 0065 runs on start),
   the R1 drop-in, `make deploy-runner`.
2. On `cmc/ci-scratch` `main`:

   ```yaml
   jobs:
     fail:
       steps:
         - echo one
         - echo two
         - echo about to fail; exit 7
   ```

3. Expected: the log ends `step 3/3 failed: exit 7`; `gitbay build show
   cmc/ci-scratch <n>` prints `failed step  3/3 echo about to fail; exit 7
   (exit 7)` and a `duration`; `gitbay build log cmc/ci-scratch <n> --step
   failed` prints `about to fail` and the failure line; the build page
   opens step 3 and "Jump to failure" scrolls to it; at phone width the
   log wraps with no sideways scroll.
4. Remove the R1 drop-in; merge Part 4. Once all four parts are in,
   delete `cmc/ci-scratch` and its fork, or keep them as the standing
   scratch pair for the next runner change.

---

# Self-review

- **Coverage.** #255: explicit trust (1.1), disposable untrusted home
  and trusted-only caches (1.2), the discard (R2.7), wiki (1.3). #258:
  `ci/` refused (2.1), reuse by trust and declared image, the default
  image's limit documented (2.2, 2.4), required contexts turning the
  gate on, shown by `settings show` and the web page, missing as
  pending in `MergeGates` (2.3), wiki (2.4). #260: public destination
  with the port off 22 (3.1), builds off loopback (3.2), the limiter's
  lockout as a unit test instead of a production test (3.3), the host
  egress table with its unit, check and deploy wiring (3.4), egress
  policy in Threat-Model, CI and Admin (3.5), the source address and
  reachability measured from a scratch build and recorded on the CI
  page (R3), with the scratch-repository rule (R1). #266: runner names the step (4.3), stored
  step and duration (4.1; duration derived), `build show` (4.4),
  `build log --step`/`--tail` (4.4), web `<details>` per step with the
  failed one open, `id="failed"`, "Jump to failure", duration beside
  finished (4.5), `pre.buildlog` wraps (4.5).
- **Placeholders.** Every code step carries the code. The one reference
  to "the number after renumbering" is the migration rule, not a gap.
- **Names across tasks.** `buildHome` (1.2) is used by `run` in 1.2 and
  4.3. `job.Trusted` (1.2), `job.SSH` (3.2). `buildSSH(public string)`
  replaces `buildSSH()` in 3.2 and the call site changes there.
  `failure`/`exitReason`/`doneArgs` (4.3). `SetBuildFailure` (4.1) is
  used in 4.2 and 4.4's test fixture. `SplitBuildLog`, `FailedSection`,
  `LogSection` (4.4) are used by `logSteps` (4.5). `SuccessBuildForTree`
  gains `image` in 2.2 and its one caller changes there.
  `GatesOut.ChecksMissing` (2.3) is rendered in `mr.html` (2.3).
  `publicSSH` (3.1) fills the claim's `ssh`, read as `job.SSH` (3.2).
  The table name `inet gitbay_runner` and the path
  `/etc/gitbay-runner/egress.nft` match across the rule, the unit, the
  check script and the Makefile (3.4).
