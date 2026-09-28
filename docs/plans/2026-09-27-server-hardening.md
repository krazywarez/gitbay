# Server hardening implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close #281, #280, #279, #282, #275 and #262 from the
2026-09-27 architecture review: an explicit TLS floor, mail that
refuses plaintext to a remote relay, mirror syncs that connect only to
an address checked at sync time, an authenticated hook socket, audited
refusals with a tamper-evident audit log, and a shared limit on git
pack generation.

**Architecture:** Six MRs, each small enough to review alone. The TLS
and mail changes are local to `cmd/gitbayd/main.go` and
`internal/mail`. Mirror sync resolves and checks the host itself, then
pins git's connection to those addresses with `http.curloptResolve`.
The hook socket gets mode 0600, a Linux peer-uid check behind build
tags, and a per-push token that sshd stores (hashed) in a new
`push_tokens` table and hookd requires. Audit rows gain a SHA-256
chain over an immutable actor column, refusals of mutating commands
are recorded through a per-actor limiter, and the daemon writes each
row to its log. A new `internal/packlimit` package holds one limiter
shared by the SSH, smart HTTP and git:// transports.

**Tech stack:** Go 1.27, SQLite (modernc, `_txlock=immediate`),
`golang.org/x/crypto/ssh`, `net/smtp`, `crypto/tls`, `syscall`
(Linux `SO_PEERCRED`), git ≥ 2.37 (`http.curloptResolve`), cobra.

**Spec:** the issue texts of #262, #275, #279, #280, #281, #282 on
krz/gitbay, and the decisions recorded in the brief for this plan set
(copied under "Decisions" below).

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
  a command reading stdin needs `ReadsStdin: true`.
  This plan adds no route, no template and no control command:
  `gitbayd admin audit verify` is a host-local cobra command, not a
  registry entry.
- New migrations: the highest today is 0059. Six plans are written in
  parallel, so numbers are pre-assigned: plan 1 uses 0060–0064, plan 2
  0065–0068, plan 3 0069–0071, plan 4 0072–0074, plan 5 0075–0077,
  plan 6 0078–0079. Whoever lands second renumbers to the next free
  number at execution time. Migrations come in `.up.sql`/`.down.sql`
  pairs. Hand-written SQL, no ORM. This plan uses 0069
  (`push_tokens`, MR 4) and 0070 (`audit_chain`, MR 5); 0071 is unused.
- Secrets travel on stdin, never argv; never logged or echoed. The
  push token is minted per receive-pack and passed only in the hook's
  environment; only its SHA-256 is stored.
- Wiki pages live in `.gitbay/wiki/` (Parity, API, Admin, Threat-Model,
  CI, Users, Performance, and the `Architecture/` folder with its
  Known-Gaps table and controls matrix). Update the page in the same MR
  that changes the behaviour it describes, and close the matching
  Known-Gaps row (`Architecture/10-Known-Gaps.org`) and controls row
  (`Architecture/09-Controls.org`).
- Writing style: plain, direct, no hype; code comments match the
  surrounding density. Comments and docs state facts, never
  before/after narration.
- The worktree may carry another session's edits; work in a fresh
  worktree per MR (`git worktree add ../gitbay-<branch> -b <branch> main`).
- `CHANGELOG.org` is written at release time (`CHANGELOG: vX.Y.Z`
  commits), not in these MRs. The release notes each MR needs are
  listed at the end of this plan.

## Decisions (from the brief)

- #275: audit refused mutating commands (rate-limited per actor); each
  audit row carries a hash of the previous row, `gitbayd admin audit
  verify` checks the chain, and rows are also written to the journal
  (a slog line on the daemon's stderr, which the unit's journal
  collects).
- #282: chmod 0600, SO_PEERCRED uid check, and a per-push token in the
  hook environment that hookd requires.
- #279: resolve and check before each sync and pin the address for git.

## Findings from the code that shape this plan

- Mirror URLs are http/https only. `runMirrorAdd`
  (`internal/control/mirrorcmd.go:58`) calls `webhook.ValidateURL`,
  which refuses any other scheme (`internal/webhook/webhook.go:31-33`).
  There is no ssh mirror URL to pin. Sync (`internal/mirror/mirror.go:76-116`)
  runs `git fetch|push <url>` with a replaced environment (no proxy
  variables), so the pin is `-c http.curloptResolve=<host>:<port>:<addrs>`
  plus `-c http.followRedirects=false` (git's default follows a
  redirect on the first request, which would reach an unchecked
  host). Sync also refuses a non-http(s) scheme, for rows that
  predate the save-time check.
- Hook environment is set in exactly one place,
  `runGit` (`internal/sshd/sshd.go:441-447`). `runGit` runs in the
  daemon (embedded SSH) and in `gitbayd shell` (system SSH mode,
  `cmd/gitbayd/system.go:97`), a separate process. The push token must
  therefore be visible across processes: it goes in SQLite, not in
  daemon memory.
- In both SSH modes the hook runs as the uid git runs as, which is the
  daemon's (`User=gitbay`, `deploy/cloud-init.yaml:210`; system mode
  logs in as the account that owns `<root>`). So the peer-uid rule is
  "equal to `os.Getuid()`".
- `audit_log.actor_id` is `REFERENCES users(id) ON DELETE SET NULL`
  (`0001_init.up.sql:186`). Hashing it would break the chain when an
  account is deleted, so migration 0070 adds `actor_ref`, the id as
  written, and the hash covers that.
- Retention deletes the oldest audit rows (`internal/store/retention.go:75`).
  Verification therefore takes the first remaining chained row's
  `prev_hash` as given.
- Audit rows are written from three kinds of process: the daemon,
  `gitbayd shell`, and `gitbayd admin …`. The chain is computed inside
  one `BEGIN IMMEDIATE` transaction (the store's DSN sets
  `_txlock=immediate`, `internal/store/store.go:47`), which serialises
  writers across processes. The journal line is written only where
  stderr is the journal: `gitbayd serve`.
- Pack generation runs in four places: SSH `gitutil.Transport`
  (`internal/gitutil/gitutil.go:39`), smart-HTTP `uploadPack`
  (`internal/httpd/smart.go:122`), git:// (`internal/gitd/gitd.go:71-77`),
  and SSH `git-upload-archive`. The info/refs advertisement
  (`smart.go:89`) and protocol-v2 `ls-refs` POSTs are ref listings and
  stay outside the limit. Web archives are already bounded by
  `archiveTimeout` and `MaxArchiveBytes` (`internal/gitutil/read.go:124-130`)
  and stay outside. receive-pack stays outside: killing or queueing
  it risks losing post-receive, which runs after the client has its
  report.
- `done` in `handleSession` (`internal/sshd/sshd.go:263-270`) closes
  when the client closes the channel *or* the server stops. A queued
  clone should give up on either; a running clone should be killed
  only when the client left, never on a restart drain.
- Dispatcher tests call `Dispatch` with a nil `Store` and expect
  refusals (`internal/control/control_test.go:192-231`). Auditing a
  refusal must tolerate a nil store.

## Order and dependencies

| # | Branch | Closes | Migration | Depends on |
|---|---|---|---|---|
| 1 | `https-tls-minimum` | #281 | — | — |
| 2 | `mail-require-tls` | #280 | — | — |
| 3 | `mirror-pin-address` | #279 | — | — |
| 4 | `hook-socket-auth` | #282 | 0069 | — |
| 5 | `audit-refusals-chain` | #275 | 0070 | — |
| 6 | `pack-limit` | #262 | — | MR 5 (both edit `sshd.Exec`) |

Cross-plan overlaps, all textual (rebase, no design dependency):

- Plan 1 (credentials-and-sessions, #256) closes connections when a key
  is removed; it edits `internal/sshd/sshd.go`, as do MRs 4, 5 and 6.
- Plan 4 (data-at-rest-and-backup, #273) decrypts `m.Token` in
  `internal/mirror/mirror.go`'s `sync`; MR 3 edits the same function.
  Whichever lands second keeps both: decryption of the token and the
  resolve-check-pin block.
- Plan 5 (web-ux, #261 doc drift) may edit the `[limits]` section of
  `Admin.org`, which MR 6 also edits (its "reserved, not yet enforced"
  line for `max_pack_bytes`/`ssh_auth_rate` is stale; leave that line
  to #261 unless it has not landed when MR 6 merges).

## File map

| File | MR | Responsibility |
|---|---|---|
| `cmd/gitbayd/main.go` | 1, 5, 6 | `serverTLS`; `st.AuditJournal`; `admin audit verify` wiring; one `packlimit.Limiter` |
| `cmd/gitbayd/tls_test.go` | 1 | TLS floor |
| `internal/config/config.go` | 2, 6 | `Mail.RequireTLS`, `Mail.TLS`, `Mail.TLSRequired`; `Limits.Pack*`, `PackLimits` |
| `internal/config/config_test.go` | 2, 6 | validation and defaults |
| `internal/mail/mail.go`, `mail_test.go` | 2 | require TLS, implicit TLS |
| `internal/webhook/webhook.go` | 3 | `CheckAddrs` |
| `internal/mirror/mirror.go`, `mirror_test.go` | 3 | resolve, check, pin |
| `internal/store/migrations/0069_push_tokens.*.sql` | 4 | table |
| `internal/store/pushtokens.go`, `pushtokens_test.go` | 4 | create, look up, delete |
| `internal/store/retention.go` | 4 | sweep expired push tokens |
| `internal/hookd/hookd.go`, `peercred_linux.go`, `peercred_other.go`, `socket_test.go` | 4 | 0600, peer uid, token check |
| `internal/sshd/sshd.go` | 4, 5, 6 | mint token; audit refused push; acquire pack slot |
| `cmd/gitbayd/hook.go` | 4 | send the token |
| `internal/store/migrations/0070_audit_chain.*.sql` | 5 | chain columns |
| `internal/store/audit.go`, `auditchain_test.go` | 5 | chained append, journal, verify |
| `internal/control/control.go`, `auditrefusal.go`, `auditrefusal_test.go` | 5 | refusal auditing |
| `cmd/gitbayd/auditverify.go` | 5 | `gitbayd admin audit verify` |
| `internal/sshd/refusal_test.go` | 5, 6 | refused push audited; busy clone |
| `e2e/audit_test.go` | 5 | `TestAuditChainVerify` |
| `internal/packlimit/packlimit.go`, `packlimit_test.go` | 6 | the limiter |
| `internal/gitutil/gitutil.go` | 6 | `Transport` takes a context |
| `internal/httpd/smart.go` (`Server`, `New`, `uploadPack`), `packlimit_test.go` | 6 | limit on POST upload-pack, `ls-refs` outside |
| `internal/gitd/gitd.go`, `gitd_test.go` | 6 | limit on git:// |
| `cmd/gitbayd/system.go` | 5, 6 | `Exec` signature |
| `deploy/clonebench.sh` | 6 | concurrent-clone benchmark |
| `.gitbay/wiki/Admin.org`, `Performance.org`, `Threat-Model.org`, `Architecture/03-Deployment.org`, `04-Trust-Boundaries.org`, `06-Data-and-Cryptography.org`, `09-Controls.org`, `10-Known-Gaps.org` | 1–6 | docs per MR |

---

# MR 1: explicit TLS minimum (branch `https-tls-minimum`, closes #281)

### Task 1.1: `serverTLS` sets TLS 1.2 on both TLS modes

**Files:**
- Create: `cmd/gitbayd/tls.go`
- Create: `cmd/gitbayd/tls_test.go`
- Modify: `cmd/gitbayd/main.go:241-242` (`case "files"`), `:301-302` (`case "acme"`)
- Modify: `.gitbay/wiki/Admin.org` (`** [http]`, lines 72-80),
  `.gitbay/wiki/Architecture/06-Data-and-Cryptography.org` (HTTPS row, line 59),
  `.gitbay/wiki/Architecture/10-Known-Gaps.org` (#281 row)

**Interfaces:**
- Produces: `func serverTLS(c *tls.Config) *tls.Config` in package `main` — sets `MinVersion = tls.VersionTLS12` on `c` (a new config when nil) and returns it.

- [ ] **Step 1: Write the failing test**

`cmd/gitbayd/tls_test.go`:

```go
package main

import (
	"crypto/tls"
	"testing"
)

// The floor is stated in code rather than inherited from the Go
// release the binary was built with (#281).
func TestServerTLSMinimum(t *testing.T) {
	if got := serverTLS(nil).MinVersion; got != tls.VersionTLS12 {
		t.Fatalf("files mode: MinVersion %#x, want %#x", got, tls.VersionTLS12)
	}
	// autocert's config carries the ALPN protocols TLS-ALPN-01 needs;
	// setting the floor must keep them.
	base := &tls.Config{NextProtos: []string{"h2", "http/1.1", "acme-tls/1"}}
	got := serverTLS(base)
	if got.MinVersion != tls.VersionTLS12 || len(got.NextProtos) != 3 {
		t.Fatalf("acme mode: %+v", got)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./cmd/gitbayd -run TestServerTLSMinimum -count=1`
Expected: build failure, `undefined: serverTLS`.

- [ ] **Step 3: Implement**

`cmd/gitbayd/tls.go`:

```go
package main

import "crypto/tls"

// serverTLS sets the HTTPS listener's protocol floor: TLS 1.2 and 1.3,
// with Go's default cipher suites.
func serverTLS(c *tls.Config) *tls.Config {
	if c == nil {
		c = &tls.Config{}
	}
	c.MinVersion = tls.VersionTLS12
	return c
}
```

In `cmd/gitbayd/main.go`, `case "files":` becomes:

```go
				case "files":
					hs.TLSConfig = serverTLS(nil)
					errCh <- hs.ListenAndServeTLS(cfg.HTTP.CertFile, cfg.HTTP.KeyFile)
```

and in `case "acme":` replace `hs.TLSConfig = m.TLSConfig()` with:

```go
					hs.TLSConfig = serverTLS(m.TLSConfig())
```

`ListenAndServeTLS` clones `TLSConfig` and loads the certificate files
into the clone, so setting it in files mode changes nothing else.

- [ ] **Step 4: Run the test and the package**

Run: `go test ./cmd/gitbayd -count=1 && go vet ./cmd/gitbayd`
Expected: PASS.

- [ ] **Step 5: Docs**

`Admin.org`, append to the `** [http]` bullet list, after the `=off=` bullet:

```org
- The HTTPS listener accepts TLS 1.2 and 1.3 only (=serverTLS= in
  =cmd/gitbayd/tls.go=), with the default cipher suites of the Go
  release the binary was built with. =openssl s_client -connect
  <host>:443 -tls1_1= fails the handshake.
```

`Architecture/06-Data-and-Cryptography.org`, HTTPS row:

```org
| HTTPS                    | TLS 1.2 minimum (=cmd/gitbayd/tls.go=), ACME or operator certificates; HSTS one year |
```

`Architecture/10-Known-Gaps.org`: delete the `#281` row.

- [ ] **Step 6: Commit**

```bash
git add cmd/gitbayd/tls.go cmd/gitbayd/tls_test.go cmd/gitbayd/main.go \
  .gitbay/wiki/Admin.org .gitbay/wiki/Architecture/06-Data-and-Cryptography.org \
  .gitbay/wiki/Architecture/10-Known-Gaps.org
git commit -S -m "https: TLS 1.2 minimum, set explicitly

Closes #281"
```

- [ ] **Step 7: MR and merge**

```bash
git push -u origin https-tls-minimum
gitbay mr create --source https-tls-minimum --target main --title "https: TLS 1.2 minimum, set explicitly"
```

After CI is green: `gitbay mr merge <n> --strategy ff`, then
`git branch -d https-tls-minimum && git push origin --delete https-tls-minimum`.

---

# MR 2: mail requires TLS to a remote relay (branch `mail-require-tls`, closes #280)

### Task 2.1: config `mail.require_tls` and `mail.tls`

**Files:**
- Modify: `internal/config/config.go:211-216` (`Mail`), `Validate` (after the `[mail] from` check, line 406-408)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Mail.RequireTLS *bool` (`toml:"require_tls,omitempty"`), `Mail.TLS string` (`toml:"tls,omitempty"`, `""`/`"starttls"`/`"implicit"`), `func (m Mail) TLSRequired() bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestMailTLSRequired(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		m    Mail
		want bool
	}{
		{Mail{SMTPHost: "smtp.example.com:587"}, true},
		{Mail{SMTPHost: "smtp.example.com"}, true},
		{Mail{SMTPHost: "localhost:25"}, false},
		{Mail{SMTPHost: "localhost"}, false},
		{Mail{SMTPHost: "127.0.0.1:25"}, false},
		{Mail{SMTPHost: "[::1]:25"}, false},
		{Mail{SMTPHost: "smtp.example.com:587", RequireTLS: &off}, false},
		{Mail{SMTPHost: "127.0.0.1:25", RequireTLS: &on}, true},
	} {
		if got := tc.m.TLSRequired(); got != tc.want {
			t.Errorf("%+v: TLSRequired = %v, want %v", tc.m, got, tc.want)
		}
	}
}
```

Add one case to the `cases` table in `TestContradictions`:

```go
		{
			"unknown mail.tls",
			minimal + "\n[mail]\nsmtp_host = \"mx.example\"\nfrom = \"gitbay@example\"\ntls = \"ssl\"\n",
			"mail.tls must be starttls or implicit",
		},
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/config -run 'TestMailTLSRequired|TestContradictions' -count=1`
Expected: build failure, `unknown field RequireTLS` / `TLSRequired undefined`.

- [ ] **Step 3: Implement**

`Mail` in `internal/config/config.go`:

```go
type Mail struct {
	SMTPHost string `toml:"smtp_host"` // host:port (port defaults to 587, 465 with tls = "implicit")
	From     string `toml:"from"`
	SMTPUser string `toml:"smtp_user,omitempty"`
	SMTPPass string `toml:"smtp_pass,omitempty"`
	// RequireTLS fails delivery when the relay does not offer STARTTLS,
	// instead of sending in clear. Unset, it is on for any relay but
	// localhost or a loopback address (TLSRequired).
	RequireTLS *bool `toml:"require_tls,omitempty"`
	// TLS is "starttls" (the default, also when empty) or "implicit":
	// TLS from the first byte, as relays on port 465 expect.
	TLS string `toml:"tls,omitempty"`
}

// TLSRequired reports whether mail must not go to the relay in clear.
func (m Mail) TLSRequired() bool {
	if m.RequireTLS != nil {
		return *m.RequireTLS
	}
	host := m.SMTPHost
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
```

In `Validate`, after the `[mail] from is required` check:

```go
	if t := c.Mail.TLS; t != "" && t != "starttls" && t != "implicit" {
		errs = append(errs, fmt.Errorf("mail.tls must be starttls or implicit, got %q", t))
	}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/config -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -S -m "config: mail.require_tls and mail.tls

Ref #280"
```

### Task 2.2: `mail.Send` refuses plaintext when TLS is required; implicit TLS

**Files:**
- Modify: `internal/mail/mail.go` (whole `Send`, package comment lines 1-3)
- Create: `internal/mail/mail_test.go`

**Interfaces:**
- Consumes: `config.Mail.TLSRequired()`, `config.Mail.TLS` (Task 2.1).
- Produces: unexported `var rootCAs *x509.CertPool` (tests set it); `Send` signature unchanged.

- [ ] **Step 1: Write the failing tests**

`internal/mail/mail_test.go`:

```go
package mail

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// fakeRelay is an SMTP server that never offers STARTTLS. Given a TLS
// config it speaks TLS from the first byte, as a port-465 relay does.
type fakeRelay struct {
	addr string
	mu   sync.Mutex
	data []string
}

func startRelay(t *testing.T, tlsCfg *tls.Config) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeRelay{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeRelay) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake\r\n")
	var body strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case inData && line == ".":
			f.mu.Lock()
			f.data = append(f.data, body.String())
			f.mu.Unlock()
			inData = false
			fmt.Fprint(conn, "250 ok\r\n")
		case inData:
			body.WriteString(line + "\n")
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			fmt.Fprint(conn, "250-fake\r\n250 SIZE 1000000\r\n")
		case line == "DATA":
			inData = true
			fmt.Fprint(conn, "354 go\r\n")
		case line == "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 ok\r\n")
		}
	}
}

func (f *fakeRelay) delivered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

func mailCfg(host string) config.Config {
	var cfg config.Config
	cfg.Mail.SMTPHost, cfg.Mail.From = host, "gitbay@example.test"
	return cfg
}

func TestRequireTLSRefusesPlaintextRelay(t *testing.T) {
	relay := startRelay(t, nil)
	cfg := mailCfg(relay.addr)
	on := true
	cfg.Mail.RequireTLS = &on
	err := Send(cfg, "a@example.test", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("Send = %v, want a refusal naming STARTTLS", err)
	}
	if n := relay.delivered(); n != 0 {
		t.Fatalf("%d message(s) sent in clear", n)
	}
}

// A loopback relay has no network to cross; the default leaves it in
// clear, which is what the e2e suite's fake relay relies on.
func TestLoopbackRelayDefaultsToPlaintext(t *testing.T) {
	relay := startRelay(t, nil)
	if err := Send(mailCfg(relay.addr), "a@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	if n := relay.delivered(); n != 1 {
		t.Fatalf("delivered %d, want 1", n)
	}
}

func TestImplicitTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	prev := rootCAs
	rootCAs = pool
	defer func() { rootCAs = prev }()

	relay := startRelay(t, &tls.Config{Certificates: ts.TLS.Certificates})
	cfg := mailCfg(relay.addr)
	cfg.Mail.TLS = "implicit"
	on := true
	cfg.Mail.RequireTLS = &on
	if err := Send(cfg, "a@example.test", "subject", "body"); err != nil {
		t.Fatal(err)
	}
	if n := relay.delivered(); n != 1 {
		t.Fatalf("delivered %d, want 1", n)
	}
}
```

`httptest`'s certificate carries `127.0.0.1` as an IP SAN, so
verification against `ServerName: "127.0.0.1"` passes.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/mail -count=1`
Expected: build failure, `undefined: rootCAs`.

- [ ] **Step 3: Implement**

`internal/mail/mail.go`:

```go
// Package mail sends transactional email over SMTP: verification codes and
// invites. The connection is encrypted with STARTTLS, or with TLS from the
// first byte when mail.tls = "implicit"; a relay that offers neither gets
// nothing unless mail.require_tls is off. PLAIN auth when credentials are
// configured.
package mail

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// rootCAs verifies the relay's certificate; nil is the system pool.
var rootCAs *x509.CertPool

// Send delivers one plain-text message. cfg.Mail.SMTPHost is host:port.
func Send(cfg config.Config, to, subject, body string) error {
	m := cfg.Mail
	if m.SMTPHost == "" || m.From == "" {
		return fmt.Errorf("[mail] smtp_host and from must be configured")
	}
	implicit := m.TLS == "implicit"
	host := m.SMTPHost
	if !strings.Contains(host, ":") {
		if implicit {
			host += ":465"
		} else {
			host += ":587"
		}
	}
	hostname, _, _ := net.SplitHostPort(host)
	tlsCfg := &tls.Config{ServerName: hostname, RootCAs: rootCAs}

	msg := strings.NewReplacer("\n", "\r\n").Replace(fmt.Sprintf(
		"From: %s\nTo: %s\nSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		m.From, to, subject, time.Now().Format(time.RFC1123Z), body))

	c, err := dial(host, hostname, implicit, tlsCfg)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", host, err)
	}
	defer c.Close()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		} else if m.TLSRequired() {
			return fmt.Errorf("%s does not offer STARTTLS and mail.require_tls is on; not sending in clear", host)
		}
	}
	if m.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", m.SMTPUser, m.SMTPPass, hostname)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// dial opens the SMTP session: plain TCP for STARTTLS, or TLS from the
// first byte.
func dial(addr, hostname string, implicit bool, tlsCfg *tls.Config) (*smtp.Client, error) {
	if !implicit {
		return smtp.Dial(addr)
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, hostname)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/mail ./internal/config -count=1 && go vet ./internal/mail`
Expected: PASS.

- [ ] **Step 5: Docs**

`Admin.org`, `** [mail]` becomes:

```org
** [mail]
- =smtp_host= (host:port; 587 assumed, 465 with =tls = "implicit"=),
  =from=, optional =smtp_user= / =smtp_pass=. Required for invite/open
  registration and self-service =email add=; in closed mode you may omit
  it entirely and assert addresses by hand (below).
- =tls= — =starttls= (default) or =implicit= (TLS from the first byte,
  for relays on 465).
- =require_tls= — with =starttls=, a relay that does not offer STARTTLS
  gets no mail: delivery fails and retries, and the admin page's Mail
  table shows the error. Unset, it is on for every relay except
  =localhost= and loopback addresses; set =false= to allow plaintext
  to a remote relay.
```

`Architecture/03-Deployment.org`, SMTP relay row:

```org
| SMTP relay             | queued mail                   | STARTTLS required for a non-local relay, or implicit TLS | =mail.require_tls=; Go's =PlainAuth= will not send credentials over plaintext to a non-local host (=internal/mail/mail.go=) |
```

`Architecture/06-Data-and-Cryptography.org`, SMTP row:

```org
| SMTP                     | STARTTLS required unless the relay is local (=mail.require_tls=), or implicit TLS (=mail.tls=) |
```

`Architecture/09-Controls.org`, "SMTP credentials protected in transit" row:

```org
| SMTP credentials protected in transit       | in place | STARTTLS required for non-local relays, implicit TLS optional (=internal/mail/mail.go=) |
```

`Architecture/10-Known-Gaps.org`: delete the `#280` row.

- [ ] **Step 6: Commit, MR, merge**

```bash
git add internal/mail .gitbay/wiki/Admin.org .gitbay/wiki/Architecture/03-Deployment.org \
  .gitbay/wiki/Architecture/06-Data-and-Cryptography.org \
  .gitbay/wiki/Architecture/09-Controls.org .gitbay/wiki/Architecture/10-Known-Gaps.org
git commit -S -m "mail: require TLS to a non-local relay; implicit TLS option

Closes #280"
git push -u origin mail-require-tls
gitbay mr create --source mail-require-tls --target main --title "mail: require TLS to a non-local relay"
```

Before merging, run the runbook's #280 check (bay1's relay must offer
STARTTLS or be local). Merge `--strategy ff` after CI, delete the branch
both places.

---

# MR 3: mirrors connect only to an address checked at sync time (branch `mirror-pin-address`, closes #279)

### Task 3.1: `webhook.CheckAddrs`

**Files:**
- Modify: `internal/webhook/webhook.go` (add after `isForbidden`, line 55)
- Create: `internal/webhook/webhook_test.go`

**Interfaces:**
- Produces: `func CheckAddrs(host string, ips []net.IP, allowLocal bool) error`.

- [ ] **Step 1: Write the failing test**

```go
package webhook

import (
	"net"
	"strings"
	"testing"
)

func TestCheckAddrs(t *testing.T) {
	public := []net.IP{net.ParseIP("203.0.113.5")}
	mixed := []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("10.1.2.3")}
	if err := CheckAddrs("git.example", public, false); err != nil {
		t.Fatalf("public: %v", err)
	}
	if err := CheckAddrs("git.example", mixed, false); err == nil || !strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("mixed: %v", err)
	}
	if err := CheckAddrs("git.example", mixed, true); err != nil {
		t.Fatalf("allow_local: %v", err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/webhook -run TestCheckAddrs -count=1`
Expected: `undefined: CheckAddrs`.

- [ ] **Step 3: Implement**

```go
// CheckAddrs refuses host when any of its resolved addresses is
// loopback, private or link-local, unless allowLocal. A caller resolves
// immediately before connecting and connects only to the addresses it
// checked.
func CheckAddrs(host string, ips []net.IP, allowLocal bool) error {
	if allowLocal {
		return nil
	}
	for _, ip := range ips {
		if isForbidden(ip) {
			return fmt.Errorf("%s resolves to private or local address %s; refusing (SSRF)", host, ip)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run it**

Run: `go test ./internal/webhook -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/webhook
git commit -S -m "webhook: CheckAddrs for callers that resolve before connecting

Ref #279"
```

### Task 3.2: mirror sync resolves, checks and pins

**Files:**
- Modify: `internal/mirror/mirror.go:30-44` (`Worker`, `New`), `:76-116` (`sync`)
- Create: `internal/mirror/mirror_test.go`

**Interfaces:**
- Consumes: `webhook.CheckAddrs` (Task 3.1).
- Produces: `Worker.Lookup func(ctx context.Context, host string) ([]net.IP, error)` (set by `New`); unexported `pinArgs(u *url.URL, ips []net.IP) []string`.

- [ ] **Step 1: Write the failing tests**

`internal/mirror/mirror_test.go`:

```go
package mirror

import (
	"context"
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// upstream serves a bare repository with one commit on main over smart
// HTTP and returns its URL and that commit.
func upstream(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	bare := filepath.Join(parent, "remote.git")
	work := filepath.Join(parent, "work")
	git(t, parent, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(t, parent, "init", "-q", "--initial-branch=main", work)
	git(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, work, "push", "-q", bare, "main")
	sha := git(t, work, "rev-parse", "HEAD")
	execPath := git(t, parent, "--exec-path")
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/remote.git", sha
}

// local returns a store with alice/app, its bare repository under root,
// and the pull mirror row for url.
func local(t *testing.T, root, mirrorURL string) (*store.Store, store.Mirror, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
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
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	dir := control.RepoDir(root, "alice", "app")
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(t, root, "init", "-q", "--bare", dir)
	if _, err := st.AddMirror(repoID, "pull", mirrorURL, "", ""); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueMirrors(900)
	if err != nil || len(due) != 1 {
		t.Fatalf("due mirrors: %v %v", due, err)
	}
	return st, due[0], dir
}

// mirror.test does not resolve; the fetch works only because git was
// pinned to the address the worker looked up and checked.
func TestSyncConnectsToTheCheckedAddress(t *testing.T) {
	remote, sha := upstream(t)
	u, _ := url.Parse(remote)
	root := t.TempDir()
	st, m, dir := local(t, root, "http://mirror.test:"+u.Port()+"/remote.git")
	var cfg config.Config
	cfg.Server.Root = root
	cfg.Webhooks.AllowLocal = true
	var asked []string
	w := &Worker{St: st, Cfg: cfg, Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}}
	if err := w.sync(m); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "refs/heads/main"); got != sha {
		t.Fatalf("main = %s, want %s", got, sha)
	}
	if !slices.Equal(asked, []string{"mirror.test"}) {
		t.Fatalf("looked up %v", asked)
	}
}

// The URL passed the check when it was saved; the answer at sync time
// is what counts.
func TestSyncRefusesAPrivateAddressAtSyncTime(t *testing.T) {
	root := t.TempDir()
	st, m, _ := local(t, root, "https://mirror.test/x.git")
	var cfg config.Config
	cfg.Server.Root = root
	w := &Worker{St: st, Cfg: cfg, Lookup: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.7")}, nil
	}}
	err := w.sync(m)
	if err == nil || !strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("sync = %v, want a refusal naming 10.0.0.7", err)
	}
}

func TestPinArgs(t *testing.T) {
	u, _ := url.Parse("https://git.example/x.git")
	got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("2001:db8::1")})
	want := []string{"-c", "http.followRedirects=false",
		"-c", "http.curloptResolve=git.example:443:203.0.113.5,[2001:db8::1]"}
	if !slices.Equal(got, want) {
		t.Fatalf("https: %q", got)
	}
	u, _ = url.Parse("http://git.example:8080/x.git")
	if got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5")}); got[3] != "http.curloptResolve=git.example:8080:203.0.113.5" {
		t.Fatalf("http with port: %q", got)
	}
	// An address literal is its own resolution; there is nothing to pin.
	u, _ = url.Parse("https://203.0.113.5/x.git")
	if got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5")}); !slices.Equal(got, []string{"-c", "http.followRedirects=false"}) {
		t.Fatalf("literal: %q", got)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/mirror -count=1`
Expected: build failure, `unknown field Lookup` / `undefined: pinArgs`.

- [ ] **Step 3: Implement**

`Worker` and `New`:

```go
type Worker struct {
	St   *store.Store
	Cfg  config.Config
	Tick time.Duration
	// Lookup resolves a mirror's host immediately before each sync.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

func New(st *store.Store, cfg config.Config) *Worker {
	tick := 10 * time.Second
	if v := os.Getenv("GITBAY_MIRROR_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Worker{St: st, Cfg: cfg, Tick: tick,
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}}
}
```

`sync` from the top through the argv; the askpass block is unchanged
and the timeout context moves above the lookup so the lookup shares it:

```go
func (w *Worker) sync(m store.Mirror) error {
	repo, err := w.St.RepoByID(m.RepoID)
	if err != nil {
		return err
	}
	dir := control.RepoDir(w.Cfg.Server.Root, repo.OwnerName, repo.Name)
	u, err := url.Parse(m.URL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("mirror URL scheme %q is not http or https", u.Scheme)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// The URL was checked when saved, but DNS can answer differently
	// now. Check what it resolves to at sync time, then let git connect
	// to exactly those addresses.
	ips, err := w.Lookup(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("resolving %s: %w", u.Hostname(), err)
	}
	if err := webhook.CheckAddrs(u.Hostname(), ips, w.Cfg.Webhooks.AllowLocal); err != nil {
		return err
	}

	env := []string{"GIT_TERMINAL_PROMPT=0", "HOME=" + w.Cfg.Server.Root}
	if m.Token != "" {
		// (askpass block unchanged)
	}

	args := append(pinArgs(u, ips), "-C", dir)
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

// pinArgs keeps git on the addresses just checked: curl's resolve list
// pins the host, and with redirects off a server cannot send git on to
// a host nobody checked. An address literal needs no pin.
func pinArgs(u *url.URL, ips []net.IP) []string {
	args := []string{"-c", "http.followRedirects=false"}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return args
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	addrs := make([]string, len(ips))
	for i, ip := range ips {
		if ip.To4() == nil {
			addrs[i] = "[" + ip.String() + "]"
		} else {
			addrs[i] = ip.String()
		}
	}
	return append(args, "-c", "http.curloptResolve="+host+":"+port+":"+strings.Join(addrs, ","))
}
```

Imports gain `net`, `net/url`, `strings`, and
`gitbay.org/gitbay/internal/webhook`. `mirror` does not import
`webhook` today; `webhook` imports only `store`, so there is no cycle.
The `// (askpass block unchanged)` line stands for lines 84-97 kept as
they are; do not type it literally.

- [ ] **Step 4: Run the package and the existing mirror e2e**

Run: `go test ./internal/mirror ./internal/webhook -count=1 && go vet ./internal/mirror`
Expected: PASS.

Run: `go test ./e2e -run TestMirrors -count=1`
Expected: PASS (its URLs are `http://127.0.0.1:<port>/…`, address
literals, so they take the no-pin branch; redirects are not used).

- [ ] **Step 5: Docs**

`Admin.org`, `** [mirrors]` last line becomes:

```org
  Mirror URLs pass the same SSRF rules as webhook targets, when saved
  and again before every sync; git then connects only to the addresses
  that were checked (=http.curloptResolve=) and does not follow
  redirects, so a mirror of a renamed repository fails until its URL
  is updated. Needs git 2.37 or later on the server.
```

`Threat-Model.org`, the paragraph under `* Network-facing request forgery`:

```org
Anything that makes the *server* open an outbound connection to a
user-supplied address — webhook delivery, GitHub-history import
=--api-base=, mirror remotes — passes the same SSRF guard: the scheme
must be http/https and, unless =webhooks.allow_local= is set, the
resolved address must not be loopback, private, or link-local. The
webhook dialer re-checks at connect time, and the mirror worker
resolves and checks before each sync and pins git to the checked
addresses, so a DNS answer that changes after validation still cannot
reach private space. Redirects are never followed.
```

`Architecture/03-Deployment.org`, Mirror URLs row:

```org
| Mirror URLs            | mirror schedule               | per URL                                      | address check at save and before each sync; git pinned to the checked addresses, no redirects (=internal/mirror/mirror.go=) |
```

`Architecture/09-Controls.org`, SSRF row:

```org
| SSRF protection on user-supplied URLs       | in place | webhooks at save and connect; mirrors at save and sync, git pinned to the checked address (=internal/mirror/mirror.go=) |
```

`Architecture/10-Known-Gaps.org`: delete the `#279` row.

- [ ] **Step 6: Commit, MR, merge**

```bash
git add internal/mirror .gitbay/wiki/Admin.org .gitbay/wiki/Threat-Model.org \
  .gitbay/wiki/Architecture/03-Deployment.org .gitbay/wiki/Architecture/09-Controls.org \
  .gitbay/wiki/Architecture/10-Known-Gaps.org
git commit -S -m "mirror: check the address before each sync and pin git to it

Closes #279"
git push -u origin mirror-pin-address
gitbay mr create --source mirror-pin-address --target main --title "mirror: check the address before each sync and pin git to it"
```

Before merging, the runbook's #279 check (git ≥ 2.37 on bay1). Merge
`--strategy ff` after CI, delete the branch both places.

---

# MR 4: authenticated hook socket (branch `hook-socket-auth`, closes #282)

### Task 4.1: `push_tokens` table and store methods

**Files:**
- Create: `internal/store/migrations/0069_push_tokens.up.sql`, `0069_push_tokens.down.sql`
- Create: `internal/store/pushtokens.go`, `internal/store/pushtokens_test.go`
- Modify: `internal/store/retention.go:47-54` (`expired` list)

**Interfaces:**
- Produces:
  - `type PushToken struct { RepoID, UserID int64; Scope string }`
  - `func (s *Store) CreatePushToken(repoID, userID int64, scope string) (string, error)` — returns the raw token; stores `HashToken(token)`; expires in 24h.
  - `func (s *Store) PushTokenByHash(hash string) (PushToken, error)` — `ErrNotFound` when absent or expired.
  - `func (s *Store) DeletePushToken(token string) error` — takes the raw token.

- [ ] **Step 1: Write the failing test**

`internal/store/pushtokens_test.go`:

```go
package store

import (
	"errors"
	"testing"
	"time"
)

func TestPushTokens(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PushTokenByHash(HashToken(token))
	if err != nil || got != (PushToken{RepoID: repoID, UserID: uid, Scope: "full"}) {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if err := s.DeletePushToken(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushTokenByHash(HashToken(token)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}

	// A token whose receive-pack never cleaned up is swept after a day.
	stale, err := s.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	swept, err := s.Sweep(Retention{}, time.Now().Add(25*time.Hour))
	if err != nil || swept["push_tokens"] != 1 {
		t.Fatalf("sweep = %v, %v", swept, err)
	}
	if _, err := s.PushTokenByHash(HashToken(stale)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after sweep: %v", err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/store -run TestPushTokens -count=1`
Expected: build failure, `s.CreatePushToken undefined`.

- [ ] **Step 3: Implement**

`0069_push_tokens.up.sql`:

```sql
-- One row per receive-pack in flight. The hook names its push by the
-- token; hookd answers only a live one. Only the SHA-256 is stored.
CREATE TABLE push_tokens (
    token_hash TEXT PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL
);
```

`0069_push_tokens.down.sql`:

```sql
DROP TABLE push_tokens;
```

`internal/store/pushtokens.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"time"
)

// PushToken is the receive-pack a hook request speaks for.
type PushToken struct {
	RepoID int64
	UserID int64
	Scope  string
}

// pushTokenTTL bounds a row whose receive-pack died before deleting it.
const pushTokenTTL = 24 * time.Hour

// CreatePushToken records a token for one receive-pack and returns it.
func (s *Store) CreatePushToken(repoID, userID int64, scope string) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.DB.Exec(
		"INSERT INTO push_tokens (token_hash, repo_id, user_id, scope, expires_at) VALUES (?, ?, ?, ?, ?)",
		hash, repoID, userID, scope, fmtTime(time.Now().Add(pushTokenTTL)))
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) PushTokenByHash(hash string) (PushToken, error) {
	var t PushToken
	err := s.DB.QueryRow(
		"SELECT repo_id, user_id, scope FROM push_tokens WHERE token_hash = ? AND expires_at > ?",
		hash, fmtTime(time.Now())).Scan(&t.RepoID, &t.UserID, &t.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return PushToken{}, ErrNotFound
	}
	return t, err
}

func (s *Store) DeletePushToken(token string) error {
	_, err := s.DB.Exec("DELETE FROM push_tokens WHERE token_hash = ?", HashToken(token))
	return err
}
```

`internal/store/retention.go`, the `expired` list gains a row:

```go
		{"web_sessions", "expires_at <= ?"},
		{"login_tokens", "expires_at <= ?"},
		{"email_tokens", "expires_at <= ?"},
		{"push_tokens", "expires_at <= ?"},
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/store -count=1`
Expected: PASS, including `TestMigrateUpDown`.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -S -m "store: push tokens for receive-pack

Ref #282"
```

### Task 4.2: hookd requires 0600, the daemon's uid, and a live push token

**Files:**
- Modify: `internal/hookd/hookd.go:34-50` (constants, `Request`), `:90-128` (`Serve`, `handle`)
- Create: `internal/hookd/peercred_linux.go`, `internal/hookd/peercred_other.go`
- Create: `internal/hookd/socket_test.go`

**Interfaces:**
- Consumes: `store.CreatePushToken`, `store.PushTokenByHash`, `store.HashToken` (Task 4.1).
- Produces: `hookd.EnvToken = "GITBAY_PUSH_TOKEN"`; `Request.Token string` (`json:"token"`); unexported `checkPeer(net.Conn) error`.

- [ ] **Step 1: Write the failing tests**

`internal/hookd/socket_test.go`:

```go
package hookd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func serveSocket(t *testing.T) (sock string, st *store.Store, repoID, uid int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if uid, err = st.CreateUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if repoID, err = st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Server.Root = t.TempDir()
	stop, err := Serve(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	return SocketPath(cfg.Server.Root), st, repoID, uid
}

func TestSocketIsOwnerOnly(t *testing.T) {
	sock, _, _, _ := serveSocket(t)
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

// A request speaks for a receive-pack sshd started, and only for the
// repository, account and scope that push was started with (#282).
func TestHookRequestNeedsItsPushToken(t *testing.T) {
	sock, st, repoID, uid := serveSocket(t)
	req := Request{Hook: "pre-receive", RepoID: repoID, UserID: uid, Scope: "full"}

	resp, err := Ask(sock, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Allow || !strings.Contains(resp.Message, "not started by this server") {
		t.Fatalf("no token: %+v", resp)
	}

	token, err := st.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	req.Token = token
	if resp, err = Ask(sock, req, nil); err != nil || !resp.Allow {
		t.Fatalf("with token: %+v, %v", resp, err)
	}

	other, err := st.CreateUser("mallory", false)
	if err != nil {
		t.Fatal(err)
	}
	forged := req
	forged.UserID = other
	if resp, err = Ask(sock, forged, nil); err != nil || resp.Allow {
		t.Fatalf("token for another account: %+v, %v", resp, err)
	}

	if err := st.DeletePushToken(token); err != nil {
		t.Fatal(err)
	}
	if resp, err = Ask(sock, req, nil); err != nil || resp.Allow {
		t.Fatalf("finished push: %+v, %v", resp, err)
	}
}
```

The peer-uid check is exercised by the same test on Linux (CI on
bay1): the test process is the daemon's uid, so a refusal there fails
the "with token" case.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/hookd -run 'TestSocketIsOwnerOnly|TestHookRequestNeedsItsPushToken' -count=1`
Expected: build failure, `unknown field Token in struct literal`.

- [ ] **Step 3: Implement**

`internal/hookd/hookd.go`, constants and `Request`:

```go
const (
	EnvSocket = "GITBAY_HOOK_SOCKET"
	EnvRepoID = "GITBAY_REPO_ID"
	EnvUserID = "GITBAY_USER_ID"
	EnvScope  = "GITBAY_KEY_SCOPE"
	// EnvToken names the receive-pack this hook runs under. sshd mints
	// it per push; hookd answers only a request carrying a live one
	// whose repository, account and scope match the request's.
	EnvToken = "GITBAY_PUSH_TOKEN"
)

type Request struct {
	Hook   string `json:"hook"` // pre-receive | post-receive
	RepoID int64  `json:"repo_id"`
	UserID int64  `json:"user_id"`
	// Scope is the pushing key's scope. The user id alone is the account
	// the key belongs to, and a deploy key grants nothing outside its
	// binding, so anything acting on another repository needs this too.
	Scope   string             `json:"scope"`
	Token   string             `json:"token"`
	Updates []policy.RefUpdate `json:"updates"`
}
```

`Serve`, after `net.Listen`:

```go
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Listen creates the socket under the process umask. Hooks run as
	// the daemon's own user; nobody else has a reason to connect.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
```

`handle`:

```go
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	if err := checkPeer(conn); err != nil {
		slog.Warn("hook socket: refused connection", "err", err)
		enc.Encode(Response{Allow: false, Message: "hook socket: " + err.Error()})
		return
	}
	var req Request
	if err := dec.Decode(&req); err != nil {
		enc.Encode(Response{Allow: false, Message: "bad hook request"})
		return
	}
	if msg := s.authorize(req); msg != "" {
		enc.Encode(Response{Allow: false, Message: msg})
		return
	}
	switch req.Hook {
	case "pre-receive":
		s.preReceive(req, dec, enc)
	case "post-receive":
		s.postReceive(req)
		enc.Encode(Response{Allow: true})
	default:
		enc.Encode(Response{Allow: false, Message: fmt.Sprintf("unknown hook %q", req.Hook)})
	}
}

// authorize ties a request to a receive-pack sshd started: its token
// must be live and name the same repository, account and key scope.
func (s *Server) authorize(req Request) string {
	if req.Token == "" {
		return "push not started by this server"
	}
	tok, err := s.st.PushTokenByHash(store.HashToken(req.Token))
	if err != nil {
		return "push not started by this server"
	}
	if tok.RepoID != req.RepoID || tok.UserID != req.UserID || tok.Scope != req.Scope {
		return "push token does not match this request"
	}
	return ""
}
```

`internal/hookd/peercred_linux.go`:

```go
//go:build linux

package hookd

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// checkPeer refuses a connection from any uid but the daemon's: git,
// and so every hook, runs as the daemon's user.
func checkPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d is not the daemon's (%d)", cred.Uid, os.Getuid())
	}
	return nil
}
```

`internal/hookd/peercred_other.go`:

```go
//go:build !linux

package hookd

import "net"

// checkPeer reads peer credentials on Linux only; elsewhere the
// socket's 0600 mode is the boundary.
func checkPeer(net.Conn) error { return nil }
```

- [ ] **Step 4: Run the package, and vet for Linux**

Run: `go test ./internal/hookd -count=1 && GOOS=linux go vet ./internal/hookd`
Expected: PASS; vet clean for both build-tag files.

- [ ] **Step 5: Commit**

```bash
git add internal/hookd
git commit -S -m "hookd: 0600 socket, peer uid check, push token required

Ref #282"
```

### Task 4.3: sshd mints the token; the hook sends it

**Files:**
- Modify: `internal/sshd/sshd.go:441-464` (`runGit`, env and transport)
- Modify: `cmd/gitbayd/hook.go:170-178` (`hookd.Ask` request)

**Interfaces:**
- Consumes: `store.CreatePushToken`, `store.DeletePushToken`, `hookd.EnvToken`, `hookd.Request.Token`.

- [ ] **Step 1: Implement in sshd**

In `runGit`, after the quota block (line 463) and before
`gitutil.Transport`:

```go
	if write {
		// hookd answers only a hook that names this receive-pack.
		token, err := st.CreatePushToken(repo.ID, user.ID, scope)
		if err != nil {
			fmt.Fprintln(stderr, "internal error")
			return protocol.ExitFailure
		}
		defer st.DeletePushToken(token)
		env = append(env, hookd.EnvToken+"="+token)
	}
```

The token is deleted when `Transport` returns, which is after
post-receive: receive-pack runs post-receive before it exits.

- [ ] **Step 2: Implement in the hook**

`cmd/gitbayd/hook.go`, the request becomes:

```go
			resp, err := hookd.Ask(sock, hookd.Request{
				Hook:    args[0],
				RepoID:  repoID,
				UserID:  userID,
				Scope:   os.Getenv(hookd.EnvScope),
				Token:   os.Getenv(hookd.EnvToken),
				Updates: updates,
			}, func(emit func(hookd.RawCommit) error) error {
				return streamIncomingCommits(updates, emit)
			})
```

- [ ] **Step 3: Build, vet, unit tests; one push e2e**

Run: `go build ./... && go vet ./... && go test ./internal/sshd ./internal/hookd ./cmd/gitbayd -count=1`
Expected: PASS.

Run: `go test ./e2e -run TestAuditAndHardening -count=1`
Expected: PASS. It pushes over SSH (including an oversized push
refused by receive-pack), so pre-receive and post-receive both go
through the token check end to end. This is the one e2e run for this
MR; CI runs the rest of the push tests.

- [ ] **Step 4: Docs**

`Architecture/03-Deployment.org`, hook.sock row:

```org
| =<root>/hook.sock=   | Unix socket       | gitbayd    | on          | mode 0600; peer uid must be the daemon's (Linux); per-push token | =internal/hookd/hookd.go= |
```

`Architecture/04-Trust-Boundaries.org`, TB5 row:

```org
| TB5 | Z3 → Z1 hook socket                | ref updates, repository id, user id, key scope, push token, commit objects | the socket is mode 0600 and, on Linux, refuses a peer whose uid is not the daemon's; a request must carry the token sshd minted for its receive-pack (stored hashed in =push_tokens=) and name the same repository, account and scope. The daemon then decides with =policy.CheckPush= and =sig.VerifyCommit= (=internal/hookd/hookd.go=) |
```

`Architecture/04-Trust-Boundaries.org`, step 2 of the push sequence
(line 57): append ", and a push token" after "key scope" in the list
of what `git receive-pack` runs with.

`Architecture/10-Known-Gaps.org`: delete the `#282` row.

- [ ] **Step 5: Commit, MR, merge**

```bash
git add internal/sshd/sshd.go cmd/gitbayd/hook.go .gitbay/wiki/Architecture
git commit -S -m "sshd: mint a push token per receive-pack; hook sends it

Closes #282"
git push -u origin hook-socket-auth
gitbay mr create --source hook-socket-auth --target main --title "hookd: authenticate the hook socket"
```

Merge `--strategy ff` after CI, delete the branch both places. Deploy
with no push in flight (see runbook): a receive-pack started by the old
daemon has no token and its post-receive is refused by the new one.

---

# MR 5: audited refusals and a hash-chained audit log (branch `audit-refusals-chain`, closes #275)

### Task 5.1: chained audit rows, the journal line, verification

**Files:**
- Create: `internal/store/migrations/0070_audit_chain.up.sql`, `0070_audit_chain.down.sql`
- Modify: `internal/store/audit.go:1-19` (`Audit`)
- Modify: `internal/store/store.go:23-30` (`Store` gains `AuditJournal`)
- Create: `internal/store/auditchain_test.go`

**Interfaces:**
- Produces:
  - `Store.AuditJournal *slog.Logger` — when set, every audit row is also logged there.
  - `type AuditChain struct { Rows, Unchained int; First, Last int64; LastHash string; BrokenAt int64; Reason string }`
  - `func (s *Store) VerifyAuditChain() (AuditChain, error)`
  - `Audit` signature unchanged.

- [ ] **Step 1: Write the failing tests**

`internal/store/auditchain_test.go`:

```go
package store

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func chainStore(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditChainIntact(t *testing.T) {
	s := chainStore(t)
	s.Audit(0, "a", map[string]any{"n": 1})
	s.Audit(0, "b", nil)
	s.Audit(0, "c", map[string]any{"n": 3})
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 3 || res.BrokenAt != 0 || res.First != 1 || res.Last != 3 || len(res.LastHash) != 64 {
		t.Fatalf("%+v", res)
	}
}

func TestAuditChainDetectsAnEditedRow(t *testing.T) {
	s := chainStore(t)
	for _, a := range []string{"a", "b", "c"} {
		s.Audit(0, a, nil)
	}
	if _, err := s.DB.Exec("UPDATE audit_log SET action = 'x' WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 2 || !strings.Contains(res.Reason, "contents") {
		t.Fatalf("%+v", res)
	}
}

func TestAuditChainDetectsARemovedRow(t *testing.T) {
	s := chainStore(t)
	for _, a := range []string{"a", "b", "c"} {
		s.Audit(0, a, nil)
	}
	if _, err := s.DB.Exec("DELETE FROM audit_log WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 3 || !strings.Contains(res.Reason, "previous hash") {
		t.Fatalf("%+v", res)
	}
}

// Retention removes the oldest rows, and deleting an account nulls
// actor_id; neither is tampering.
func TestAuditChainSurvivesRetentionAndAccountDeletion(t *testing.T) {
	s := chainStore(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Audit(0, "a", nil)
	s.Audit(uid, "b", nil)
	s.Audit(0, "c", nil)
	if _, err := s.DB.Exec("DELETE FROM audit_log WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM users WHERE id = ?", uid); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 0 || res.First != 2 || res.Last != 3 {
		t.Fatalf("%+v", res)
	}
}

// Rows written before migration 0070 carry no hash; the chain starts
// after them, and a hashless row after that start is a break.
func TestAuditChainLegacyRows(t *testing.T) {
	s := chainStore(t)
	if _, err := s.DB.Exec("INSERT INTO audit_log (action) VALUES ('legacy')"); err != nil {
		t.Fatal(err)
	}
	s.Audit(0, "a", nil)
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Unchained != 1 || res.BrokenAt != 0 || res.First != 2 {
		t.Fatalf("%+v", res)
	}
	if _, err := s.DB.Exec("INSERT INTO audit_log (action) VALUES ('injected')"); err != nil {
		t.Fatal(err)
	}
	if res, _ = s.VerifyAuditChain(); res.BrokenAt != 3 {
		t.Fatalf("hashless row after the chain: %+v", res)
	}
}

func TestAuditJournal(t *testing.T) {
	s := chainStore(t)
	var buf bytes.Buffer
	s.AuditJournal = slog.New(slog.NewTextHandler(&buf, nil))
	s.Audit(0, "cmd repo create", map[string]any{"argv": []string{"a/b"}})
	line := buf.String()
	for _, want := range []string{"msg=audit", "action=\"cmd repo create\"", "id=1", "hash="} {
		if !strings.Contains(line, want) {
			t.Fatalf("journal line %q lacks %q", line, want)
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/store -run 'TestAuditChain|TestAuditJournal' -count=1`
Expected: build failure, `s.VerifyAuditChain undefined`.

- [ ] **Step 3: Implement**

`0070_audit_chain.up.sql`:

```sql
-- Each row carries the hash of the row before it. actor_ref is the actor
-- id as written: actor_id is set to NULL when the account is deleted,
-- and the hash must not change with it. Rows written before this
-- migration keep an empty hash; the chain starts after them.
ALTER TABLE audit_log ADD COLUMN actor_ref INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit_log ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN hash TEXT NOT NULL DEFAULT '';
UPDATE audit_log SET actor_ref = COALESCE(actor_id, 0);
```

`0070_audit_chain.down.sql`:

```sql
ALTER TABLE audit_log DROP COLUMN hash;
ALTER TABLE audit_log DROP COLUMN prev_hash;
ALTER TABLE audit_log DROP COLUMN actor_ref;
```

`internal/store/store.go`, `Store` gains:

```go
	// AuditJournal, when set, receives a copy of every audit row. The
	// daemon sets it to its own logger, whose output the service
	// journal keeps outside the database.
	AuditJournal *slog.Logger
```

(`store.go` imports `log/slog`.)

`internal/store/audit.go`, replacing `Audit` (lines 5-19) and adding
the chain helpers; `AuditEntry`, `AuditFilter` and `AuditEntries` stay
as they are:

```go
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Audit appends to the security feed. Events are the product feed; this
// records who did what, from where, for an operator. actorID 0 means the
// host admin (gitbayd admin commands) or an unauthenticated source.
func (s *Store) Audit(actorID int64, action string, data map[string]any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	id, createdAt, hash, err := s.appendAudit(actorID, action, string(raw), time.Now())
	if s.AuditJournal == nil {
		return
	}
	if err != nil {
		s.AuditJournal.Error("audit: append", "action", action, "err", err)
		return
	}
	s.AuditJournal.Info("audit", "id", id, "actor", actorID, "action", action,
		"data", string(raw), "created_at", createdAt, "hash", hash)
}

// appendAudit writes one row and its chain hash in one transaction. The
// store begins every transaction IMMEDIATE, so two writers — the daemon
// and a gitbayd admin command, say — cannot both read the same last
// hash.
func (s *Store) appendAudit(actorID int64, action, data string, now time.Time) (int64, string, string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, "", "", err
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow("SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1").Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", err
	}
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	createdAt := fmtTime(now)
	res, err := tx.Exec(
		"INSERT INTO audit_log (actor_id, actor_ref, action, data_json, created_at, prev_hash) VALUES (?, ?, ?, ?, ?, ?)",
		actor, actorID, action, data, createdAt, prev)
	if err != nil {
		return 0, "", "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", "", err
	}
	hash := auditHash(prev, id, actorID, action, createdAt, data)
	if _, err := tx.Exec("UPDATE audit_log SET hash = ? WHERE id = ?", hash, id); err != nil {
		return 0, "", "", err
	}
	return id, createdAt, hash, tx.Commit()
}

// auditHash covers every column an operator reads, plus the previous
// row's hash. A JSON array keeps field boundaries unambiguous.
func auditHash(prev string, id, actor int64, action, createdAt, data string) string {
	b, _ := json.Marshal([]any{prev, id, actor, action, createdAt, data})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AuditChain is what VerifyAuditChain found.
type AuditChain struct {
	Rows      int   // rows read
	Unchained int   // rows from before migration 0070, which carry no hash
	First     int64 // first chained row; its prev_hash is taken as given, since retention may have removed the row it names
	Last      int64
	LastHash  string
	BrokenAt  int64 // 0 when the chain is intact
	Reason    string
}

// VerifyAuditChain recomputes every row's hash in id order and stops at
// the first row that does not match. It cannot see rows removed from
// the end of the table; the journal copy covers those.
func (s *Store) VerifyAuditChain() (AuditChain, error) {
	rows, err := s.DB.Query(`SELECT id, actor_ref, action, data_json, created_at, prev_hash, hash
		FROM audit_log ORDER BY id`)
	if err != nil {
		return AuditChain{}, err
	}
	defer rows.Close()
	var res AuditChain
	for rows.Next() {
		var (
			id, actor                          int64
			action, data, createdAt, prev, hash string
		)
		if err := rows.Scan(&id, &actor, &action, &data, &createdAt, &prev, &hash); err != nil {
			return res, err
		}
		res.Rows++
		switch {
		case hash == "" && res.First == 0:
			res.Unchained++
			continue
		case hash == "":
			res.BrokenAt, res.Reason = id, "row has no hash after the chain began"
		case res.First != 0 && prev != res.LastHash:
			res.BrokenAt, res.Reason = id, "previous hash does not match: a row before it was removed or changed"
		case auditHash(prev, id, actor, action, createdAt, data) != hash:
			res.BrokenAt, res.Reason = id, "row contents do not match its hash"
		}
		if res.BrokenAt != 0 {
			return res, nil
		}
		if res.First == 0 {
			res.First = id
		}
		res.Last, res.LastHash = id, hash
	}
	return res, rows.Err()
}
```

The previous `Audit` ignored every error; it still returns nothing,
and reports an append failure only where there is a journal to report
it to.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/store -count=1`
Expected: PASS, `TestMigrateUpDown` and `TestSweep*` included (the
retention sweep still deletes by `created_at`).

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -S -m "store: hash-chained audit rows, journal copy, chain verification

Ref #275"
```

### Task 5.2: refused mutating commands are audited, rate-limited per actor

**Files:**
- Create: `internal/control/auditrefusal.go`, `internal/control/auditrefusal_test.go`
- Modify: `internal/control/control.go:153-191` (`Dispatch` from the scope check to the end)

**Interfaces:**
- Produces: `func AuditRefused(st *store.Store, actorID int64, action string, data map[string]any)` — records at most `refusalsPerMinute` (10) rows per actor per minute, then one `refused.throttled` row for the rest of that minute; a nil store records nothing.
- Dispatch audit actions: `"cmd <path>"` on success (unchanged), `"refused <path>"` on exit 4 or exit 3, for commands that are not `ReadOnly`, with data `{"argv", "source", "exit"}`.

- [ ] **Step 1: Write the failing tests**

`internal/control/auditrefusal_test.go`:

```go
package control

import (
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestRefusedWritesAreAudited(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob := store.User{ID: bobID, Username: "bob"}

	c, _ := pruneCtx(st, t.TempDir(), bob)
	if code := Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"}); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	// A refused read is not a write attempt.
	c, _ = pruneCtx(st, t.TempDir(), bob)
	if code := Dispatch(c, []string{"audit"}); code != protocol.ExitDenied {
		t.Fatalf("audit: exit %d", code)
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "refused repo delete" || got[0].Actor != "bob" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestRefusalAuditIsRateLimited(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	for range refusalsPerMinute + 5 {
		c, _ := pruneCtx(st, t.TempDir(), store.User{ID: bobID, Username: "bob"})
		Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"})
	}
	refused, _ := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused ", Limit: 100})
	throttled, _ := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused.throttled", Limit: 100})
	if len(refused) != refusalsPerMinute || len(throttled) != 1 {
		t.Fatalf("%d refused rows, %d throttled rows", len(refused), len(throttled))
	}
}
```

`AuditEntries` with prefix `"refused "` (trailing space) excludes
`refused.throttled`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/control -run 'TestRefusedWritesAreAudited|TestRefusalAuditIsRateLimited' -count=1`
Expected: build failure, `undefined: refusals`.

- [ ] **Step 3: Implement the limiter**

`internal/control/auditrefusal.go`:

```go
package control

import (
	"sync"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// refusalsPerMinute bounds audit rows for refused writes per actor. A
// probe is what these rows record, and a loop of probes must not grow
// the table without bound.
const refusalsPerMinute = 10

type refusalLimiter struct {
	mu   sync.Mutex
	seen map[int64]*refusalWindow
}

type refusalWindow struct {
	start time.Time
	n     int
}

var refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}

// allow reports whether to record this refusal, and whether it is the
// first one past the limit in the current minute.
func (l *refusalLimiter) allow(actor int64, now time.Time) (record, firstDropped bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 4096 {
		for k, w := range l.seen {
			if now.Sub(w.start) >= time.Minute {
				delete(l.seen, k)
			}
		}
	}
	w := l.seen[actor]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &refusalWindow{start: now}
		l.seen[actor] = w
	}
	w.n++
	return w.n <= refusalsPerMinute, w.n == refusalsPerMinute+1
}

// AuditRefused records a refused attempt to change something. Past the
// per-actor limit it records one refused.throttled row a minute and
// drops the rest. Dispatcher tests run without a store.
func AuditRefused(st *store.Store, actorID int64, action string, data map[string]any) {
	if st == nil {
		return
	}
	switch record, first := refusals.allow(actorID, time.Now()); {
	case record:
		st.Audit(actorID, action, data)
	case first:
		st.Audit(actorID, "refused.throttled", map[string]any{"limit_per_minute": refusalsPerMinute})
	}
}
```

- [ ] **Step 4: Route every Dispatch refusal through it**

In `internal/control/control.go`, everything in `Dispatch` from the
scope check (line 154) to the end moves into `runChecked`, and
`Dispatch` ends:

```go
	c.Argv = args
	code := runChecked(c, cmd, args)
	if !cmd.ReadOnly {
		data := map[string]any{"argv": auditArgs(args), "source": c.Source}
		switch code {
		case protocol.ExitOK:
			// Every successful mutating command lands in the audit log.
			c.Store.Audit(c.User.ID, "cmd "+joinPath(cmd.Path), data)
		case protocol.ExitDenied, protocol.ExitNotFound:
			// So does every refused one: probing leaves a trace.
			data["exit"] = code
			AuditRefused(c.Store, c.User.ID, "refused "+joinPath(cmd.Path), data)
		}
	}
	return code
}

// runChecked applies the dispatcher's own gates, then runs the command.
func runChecked(c *Ctx, cmd Command, args []string) int {
	// A runner-scoped key reaches the runner protocol and nothing else, so
	// the key a CI host holds cannot administer the instance.
	if c.Scope != "full" && !(c.Scope == "runner" && cmd.Path[0] == "runner") {
		return c.fail(protocol.ExitDenied, "this key's scope (%s) does not allow control commands; use a key added with --scope full", c.Scope)
	}
	if c.ReadOnly && !cmd.ReadOnly {
		return c.fail(protocol.ExitDenied, "this token is read-only; %s modifies state — mint one with --scope full", joinPath(cmd.Path))
	}
	// The SSH listener refuses a disabled account before it gets here; the
	// API and the web reach Dispatch directly, so the check lives here too.
	if c.User.Disabled {
		return c.fail(protocol.ExitDenied, "this account is disabled; ask an instance admin to enable it")
	}
	// The admin noun is gated here as well as in each handler, so a new
	// admin command that forgets requireInstanceAdmin is still refused.
	if cmd.Path[0] == "admin" && !c.User.IsAdmin {
		return c.fail(protocol.ExitDenied, "admin commands are for instance admins; ask one")
	}
	if c.User.Pending && !pendingAllowed(cmd.Path) {
		return c.fail(protocol.ExitDenied,
			"your account is not active yet: verify your email first (email verify <code>, or ask for the mail again with email add)")
	}
	if code := limitWrites(c, cmd); code >= 0 {
		return code
	}
	if !cmd.ReadsStdin {
		c.Stdin = emptyReader{}
	}
	return cmd.Run(c, args)
}
```

The gates and their messages are unchanged; only their position moves.
A successful command's audit data keeps exactly `argv` and `source`.

- [ ] **Step 5: Run the package**

Run: `go test ./internal/control -count=1`
Expected: PASS, including `TestAdminNounGatedInDispatch` and
`TestRefusalsHonourJSON` (nil store: `AuditRefused` returns early).

- [ ] **Step 6: Commit**

```bash
git add internal/control
git commit -S -m "control: audit refused mutating commands, rate-limited per actor

Ref #275"
```

### Task 5.3: refused pushes, the daemon's journal, `gitbayd admin audit verify`

**Files:**
- Modify: `internal/sshd/sshd.go:355-360` (`Exec`, git transport case)
- Create: `internal/sshd/refusal_test.go`
- Create: `cmd/gitbayd/auditverify.go`
- Modify: `cmd/gitbayd/main.go:138-142` (`serveCmd`, after `openStore`), `:416` (`admin audit` registration)
- Modify: `e2e/audit_test.go` (new test `TestAuditChainVerify`)

**Interfaces:**
- Consumes: `control.AuditRefused` (Task 5.2), `store.AuditJournal`, `store.VerifyAuditChain` (Task 5.1).
- Produces: `func auditVerifyCmd() *cobra.Command` in package `main`.

- [ ] **Step 1: Write the failing sshd test**

`internal/sshd/refusal_test.go`:

```go
package sshd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// execFixture: alice owns the public alice/app; bob has no grant on it.
func execFixture(t *testing.T) (config.Config, *store.Store, store.User) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", alice, "app", "public"); err != nil {
		t.Fatal(err)
	}
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.UserByID(bobID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.Root = t.TempDir()
	return cfg, st, bob
}

func TestRefusedPushIsAudited(t *testing.T) {
	cfg, st, bob := execFixture(t)
	var out, errOut bytes.Buffer
	code := Exec(cfg, st, bob, "full", "SHA256:test", control.Term{}, "git-receive-pack alice/app",
		strings.NewReader(""), &out, &errOut, nil, nil)
	if code != protocol.ExitDenied {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused git-receive-pack", Limit: 5})
	if err != nil || len(got) != 1 || got[0].Actor != "bob" || !strings.Contains(got[0].Data, "alice/app") {
		t.Fatalf("entries %+v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/sshd -run TestRefusedPushIsAudited -count=1`
Expected: FAIL, `entries [] <nil>`.

- [ ] **Step 3: Implement in `Exec`**

```go
		case "git-upload-pack", "git-receive-pack", "git-upload-archive":
			if user.Pending {
				fmt.Fprintln(stderr, "your account is not active yet: verify your email first")
				return protocol.ExitDenied
			}
			code := runGit(cfg, st, user, scope, argv, stdin, stdout, stderr)
			if argv[0] == "git-receive-pack" && (code == protocol.ExitDenied || code == protocol.ExitNotFound) {
				control.AuditRefused(st, user.ID, "refused git-receive-pack",
					map[string]any{"argv": argv[1:], "source": source})
			}
			return code
```

Run: `go test ./internal/sshd -count=1`
Expected: PASS.

- [ ] **Step 4: Write the failing e2e test**

Append to `e2e/audit_test.go` (imports gain `path/filepath` — already
there — and `gitbay.org/gitbay/internal/store`):

```go
// The audit log is a hash chain: gitbayd admin audit verify passes on
// an untouched log and names the first row that was edited (#275).
func TestAuditChainVerify(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if out := inst.admin(t, "admin", "audit", "verify"); !strings.Contains(out, "intact") {
		t.Fatalf("verify: %s", out)
	}

	st, err := store.Open(filepath.Join(inst.root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := st.DB.QueryRow("SELECT id FROM audit_log WHERE action = 'cmd repo create'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("UPDATE audit_log SET data_json = '{}' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out := inst.forgedAdminErr(t, "admin", "audit", "verify")
	if !strings.Contains(out, fmt.Sprintf("row %d", id)) {
		t.Fatalf("verify after edit: %s", out)
	}
}
```

(`fmt` is added to the file's imports.)

- [ ] **Step 5: Run it and see it fail**

Run: `go build ./... && go test ./e2e -run TestAuditChainVerify -count=1`
Expected: FAIL, `gitbayd [admin audit verify]: exit status 2` — the
host `audit` command reads `verify` as an unknown argument.

- [ ] **Step 6: Implement the verify command and the journal**

`cmd/gitbayd/auditverify.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
)

// auditVerifyCmd recomputes the audit log's hash chain. A break names
// the first row that does not match; rows removed from the end of the
// log leave no break, and the journal copy is the record for those.
func auditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "check the audit log's hash chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			res, err := st.VerifyAuditChain()
			if err != nil {
				return err
			}
			fmt.Printf("read %d rows (%d from before the chain)\n", res.Rows, res.Unchained)
			if res.BrokenAt != 0 {
				return fmt.Errorf("chain broken at row %d: %s", res.BrokenAt, res.Reason)
			}
			if res.Last == 0 {
				fmt.Println("no chained rows yet")
				return nil
			}
			fmt.Printf("chain intact from row %d to row %d\nlast hash %s\n", res.First, res.Last, res.LastHash)
			return nil
		},
	}
}
```

`cmd/gitbayd/main.go`, in `adminCmd`, replace the `hostCmd("audit …")`
entry in `admin.AddCommand(…)` with a variable built before the call:

```go
	auditCmd := hostCmd("audit [--limit n] [--json]", "print the security audit log, newest first", "audit")
	auditCmd.AddCommand(auditVerifyCmd())
```

and pass `auditCmd` in its place. cobra resolves `verify` as the child
before the parent's passthrough arguments are considered, so
`gitbayd admin audit --limit 5` is unchanged.

In `serveCmd`, after `defer st.Close()`:

```go
			// The daemon's stderr is the service journal: a copy of each
			// audit row outside the database the daemon can write.
			st.AuditJournal = slog.Default()
```

`gitbayd shell` and host `gitbayd admin` commands leave it unset:
their stderr is the SSH client or the operator's terminal (open
question 1).

- [ ] **Step 7: Run it**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbayd ./internal/sshd ./internal/control ./internal/store -count=1 && go test ./e2e -run TestAuditChainVerify -count=1`
Expected: PASS.

- [ ] **Step 8: Docs**

`Admin.org`, under `* Audit and account control`, the first paragraph
becomes:

```org
The audit log is the security feed (events are the product feed): every
successful mutating command with its argv and source credential (SSH key
fingerprint or API), every refused one (exit 3 or 4) as =refused
<command>=, refused pushes as =refused git-receive-pack=, registrations,
admin actions, force-pushes, and auth failures/throttling. Refusals are
recorded up to ten a minute per account; past that, one
=refused.throttled= row stands for the rest of the minute. Secrets
never appear — they travel on stdin, never in argv.

Each row carries the SHA-256 of the row before it. =gitbayd admin audit
verify= recomputes the chain and names the first row that was edited or
whose predecessor was removed; it prints the last hash, which an
operator can note elsewhere. Retention removing the oldest rows is not
a break. Rows removed from the end leave no break, so the daemon also
logs every row to its journal (=journalctl -u gitbayd -g '^.*msg=audit'=),
outside the database it writes. Rows written before the chain existed
are counted and skipped.
```

and add to the command block:

```org
gitbayd admin audit verify           # check the hash chain; exit 1 names the first bad row
```

`Architecture/09-Controls.org`, the two Logging rows:

```org
| Denied attempts audited                     | in place | refused mutating commands and pushes, ten a minute per actor (=internal/control/auditrefusal.go=) |
| Audit log tamper resistance                 | partial  | hash chain checked by =gitbayd admin audit verify=; every row copied to the journal; the table itself is writable by the daemon user |
```

`Architecture/06-Data-and-Cryptography.org`, the Audit and feed row
(line 21): append ", a hash chain (=prev_hash=, =hash=)" to its
description column.

`Architecture/10-Known-Gaps.org`: delete the `#275` row.

- [ ] **Step 9: Commit, MR, merge**

```bash
git add internal/sshd cmd/gitbayd e2e/audit_test.go .gitbay/wiki
git commit -S -m "audit: refused pushes, journal copy, admin audit verify

Closes #275"
git push -u origin audit-refusals-chain
gitbay mr create --source audit-refusals-chain --target main --title "audit: record refusals; hash-chain the log"
```

Merge `--strategy ff` after CI, delete the branch both places.

---

# MR 6: one limit on pack generation (branch `pack-limit`, closes #262)

### Task 6.1: `internal/packlimit`

**Files:**
- Create: `internal/packlimit/packlimit.go`, `internal/packlimit/packlimit_test.go`

**Interfaces:**
- Produces:
  - `var ErrBusy error`, `var ErrGone error`
  - `func New(max, per, queue int, wait time.Duration) *Limiter` — nil when `max <= 0` (no limit); `per <= 0` means no per-principal cap; `queue` is how many may wait (0: none).
  - `func (l *Limiter) Acquire(done <-chan struct{}, principal string) (release func(), err error)` — nil receiver never waits; `release` is idempotent.

- [ ] **Step 1: Write the failing tests**

```go
package packlimit

import (
	"errors"
	"testing"
	"time"
)

func TestGlobalCap(t *testing.T) {
	l := New(2, 0, 0, time.Second)
	r1, err1 := l.Acquire(nil, "a")
	r2, err2 := l.Acquire(nil, "b")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if _, err := l.Acquire(nil, "c"); !errors.Is(err, ErrBusy) {
		t.Fatalf("third with no queue: %v", err)
	}
	r1()
	r1() // a second release is a no-op
	r3, err := l.Acquire(nil, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(nil, "d"); !errors.Is(err, ErrBusy) {
		t.Fatalf("double release freed two slots: %v", err)
	}
	r2()
	r3()
}

func TestPerPrincipalCap(t *testing.T) {
	l := New(4, 1, 4, 50*time.Millisecond)
	ra, err := l.Acquire(nil, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer ra()
	if _, err := l.Acquire(nil, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second for a: %v", err)
	}
	rb, err := l.Acquire(nil, "b")
	if err != nil {
		t.Fatalf("b blocked by a: %v", err)
	}
	rb()
}

func TestWaiterGetsReleasedSlot(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	got := make(chan error, 1)
	go func() {
		r, err := l.Acquire(nil, "b")
		if err == nil {
			r()
		}
		got <- err
	}()
	waitQueued(t, l, 1)
	r1()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never got the slot")
	}
}

func TestQueueIsBounded(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	defer r1()
	go l.Acquire(nil, "b")
	waitQueued(t, l, 1)
	if _, err := l.Acquire(nil, "c"); !errors.Is(err, ErrBusy) {
		t.Fatalf("queue over its bound: %v", err)
	}
}

// A principal cannot fill the queue on its own.
func TestPrincipalQueueIsBounded(t *testing.T) {
	l := New(1, 1, 8, 5*time.Second)
	r1, _ := l.Acquire(nil, "x")
	defer r1()
	go l.Acquire(nil, "a")
	waitQueued(t, l, 1)
	if _, err := l.Acquire(nil, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second waiter for a: %v", err)
	}
}

func TestClientGoneWhileQueued(t *testing.T) {
	l := New(1, 0, 1, 5*time.Second)
	r1, _ := l.Acquire(nil, "a")
	defer r1()
	done := make(chan struct{})
	close(done)
	if _, err := l.Acquire(done, "b"); !errors.Is(err, ErrGone) {
		t.Fatalf("got %v, want ErrGone", err)
	}
}

func TestWaitRunsOut(t *testing.T) {
	l := New(1, 0, 1, 20*time.Millisecond)
	r1, _ := l.Acquire(nil, "a")
	defer r1()
	if _, err := l.Acquire(nil, "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

func TestNilLimiterNeverWaits(t *testing.T) {
	var l *Limiter
	if l = New(0, 1, 1, time.Second); l != nil {
		t.Fatal("max 0 should mean no limit")
	}
	r, err := l.Acquire(nil, "a")
	if err != nil {
		t.Fatal(err)
	}
	r()
}

func waitQueued(t *testing.T, l *Limiter, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		q := l.queued
		l.mu.Unlock()
		if q == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue never reached %d", n)
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/packlimit -count=1`
Expected: `no non-test Go files` / build failure.

- [ ] **Step 3: Implement**

`internal/packlimit/packlimit.go`:

```go
// Package packlimit bounds concurrent git pack generation. upload-pack
// and upload-archive over SSH, smart HTTP and git:// draw on one
// budget: a global cap, a cap per principal (an account, or a client
// address on the anonymous transports), and a bounded queue whose
// waiters give up after a fixed wait or when the client goes away.
// Waiters are not served in order; the wait bounds how long any one
// of them waits.
package packlimit

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrBusy = errors.New("the server is busy generating packs for other clients; try again in a minute")
	ErrGone = errors.New("client went away while queued")
)

type Limiter struct {
	max, per, queue int
	wait            time.Duration

	mu      sync.Mutex
	running int
	queued  int
	held    map[string]int // running, per principal
	waiting map[string]int // queued, per principal
	changed chan struct{}  // closed and replaced on every release
}

// New returns a limiter, or nil — no limit — when max is not positive.
func New(max, per, queue int, wait time.Duration) *Limiter {
	if max <= 0 {
		return nil
	}
	return &Limiter{max: max, per: per, queue: queue, wait: wait,
		held: map[string]int{}, waiting: map[string]int{}, changed: make(chan struct{})}
}

// Acquire takes a slot for principal, queueing when none is free.
// release is called once git has exited. done, when it closes, ends
// the wait.
func (l *Limiter) Acquire(done <-chan struct{}, principal string) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	if l.fits(principal) {
		l.take(principal)
		l.mu.Unlock()
		return l.releaser(principal), nil
	}
	if l.queued >= l.queue || (l.per > 0 && l.waiting[principal] >= l.per) {
		l.mu.Unlock()
		return nil, ErrBusy
	}
	l.queued++
	l.waiting[principal]++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.queued--
		if l.waiting[principal]--; l.waiting[principal] == 0 {
			delete(l.waiting, principal)
		}
		l.mu.Unlock()
	}()

	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	for {
		l.mu.Lock()
		if l.fits(principal) {
			l.take(principal)
			l.mu.Unlock()
			return l.releaser(principal), nil
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil, ErrBusy
		case <-done:
			return nil, ErrGone
		}
	}
}

func (l *Limiter) fits(principal string) bool {
	return l.running < l.max && (l.per <= 0 || l.held[principal] < l.per)
}

func (l *Limiter) take(principal string) {
	l.running++
	l.held[principal]++
}

func (l *Limiter) releaser(principal string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.running--
			if l.held[principal]--; l.held[principal] == 0 {
				delete(l.held, principal)
			}
			close(l.changed)
			l.changed = make(chan struct{})
		})
	}
}
```

- [ ] **Step 4: Run it, with the race detector**

Run: `go test -race ./internal/packlimit -count=3`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/packlimit
git commit -S -m "packlimit: global and per-principal limit with a bounded queue

Ref #262"
```

### Task 6.2: config knobs

**Files:**
- Modify: `internal/config/config.go:187-209` (`Limits`), `Validate` (after the `max_snippets_per_user` check, line 344-346)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Limits.PackConcurrency`, `PackPerPrincipal`, `PackQueue int`, `PackQueueWait string`; `func (l Limits) PackLimits() (max, per, queue int, wait time.Duration)`; constants `DefaultPackConcurrency = 3`, `DefaultPackPerPrincipal = 2`, `DefaultPackQueue = 32`, `DefaultPackQueueWait = time.Minute`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPackLimits(t *testing.T) {
	max, per, queue, wait := Limits{}.PackLimits()
	if max != DefaultPackConcurrency || per != DefaultPackPerPrincipal || queue != DefaultPackQueue || wait != DefaultPackQueueWait {
		t.Fatalf("defaults: %d %d %d %s", max, per, queue, wait)
	}
	max, per, queue, wait = Limits{PackConcurrency: -1, PackPerPrincipal: -1, PackQueue: -1, PackQueueWait: "5s"}.PackLimits()
	if max != 0 || per != 0 || queue != 0 || wait != 5*time.Second {
		t.Fatalf("off: %d %d %d %s", max, per, queue, wait)
	}
	max, per, queue, _ = Limits{PackConcurrency: 8, PackPerPrincipal: 3, PackQueue: 64}.PackLimits()
	if max != 8 || per != 3 || queue != 64 {
		t.Fatalf("set: %d %d %d", max, per, queue)
	}
}
```

(`config_test.go` imports gain `time`.) And one `TestContradictions` case:

```go
		{
			"bad pack_queue_wait",
			minimal + "\n[limits]\npack_queue_wait = \"soon\"\n",
			"limits.pack_queue_wait",
		},
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/config -run 'TestPackLimits|TestContradictions' -count=1`
Expected: build failure, `PackLimits undefined`.

- [ ] **Step 3: Implement**

Add to `Limits`:

```go
	// PackConcurrency caps git pack generation (upload-pack and
	// upload-archive) running at once across SSH, smart HTTP and git://.
	// PackPerPrincipal caps it per account, or per client address on the
	// anonymous transports. PackQueue is how many may wait for a slot,
	// for at most PackQueueWait ("60s"). For the three counts 0 takes the
	// default and a negative value turns that bound off.
	PackConcurrency  int    `toml:"pack_concurrency"`
	PackPerPrincipal int    `toml:"pack_per_principal"`
	PackQueue        int    `toml:"pack_queue"`
	PackQueueWait    string `toml:"pack_queue_wait"`
```

After `DefaultWriteRate`:

```go
// Pack generation defaults for a four-core host: a full clone of a large
// repository runs git at about 1.5 cores (Performance wiki page).
const (
	DefaultPackConcurrency  = 3
	DefaultPackPerPrincipal = 2
	DefaultPackQueue        = 32
	DefaultPackQueueWait    = time.Minute
)
```

After `Limits`:

```go
// PackLimits resolves the pack_* settings for packlimit.New. A zero
// count is no bound.
func (l Limits) PackLimits() (max, per, queue int, wait time.Duration) {
	pick := func(v, def int) int {
		switch {
		case v == 0:
			return def
		case v < 0:
			return 0
		}
		return v
	}
	wait = DefaultPackQueueWait
	if d, err := time.ParseDuration(l.PackQueueWait); err == nil && d > 0 {
		wait = d
	}
	return pick(l.PackConcurrency, DefaultPackConcurrency),
		pick(l.PackPerPrincipal, DefaultPackPerPrincipal),
		pick(l.PackQueue, DefaultPackQueue), wait
}
```

In `Validate`:

```go
	if w := c.Limits.PackQueueWait; w != "" {
		if d, err := time.ParseDuration(w); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("limits.pack_queue_wait %q must be a positive duration such as 60s", w))
		}
	}
```

- [ ] **Step 4: Run the package**

Run: `go test ./internal/config -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -S -m "config: pack_concurrency, pack_per_principal, pack_queue, pack_queue_wait

Ref #262"
```

### Task 6.3: SSH transports acquire a slot and die with their client

**Files:**
- Modify: `internal/gitutil/gitutil.go:35-56` (`Transport` takes a context)
- Modify: `internal/sshd/sshd.go:34-71` (`Server.packs`, `New`), `:299-313` (`runExec`), `:339-385` (`Exec`), `:387-468` (`runGit`)
- Modify: `cmd/gitbayd/system.go:97`
- Modify: `internal/sshd/sshd_test.go:65` (`New(cfg, st, nil)`)
- Modify: `internal/sshd/refusal_test.go` (Exec call gains `nil` packs; new busy test)

**Interfaces:**
- Consumes: `packlimit.Limiter`, `packlimit.New` (Task 6.1).
- Produces:
  - `func Transport(ctx context.Context, service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string, maxPack int64) error`
  - `func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) (*Server, error)`
  - `func Exec(cfg config.Config, st *store.Store, packs *packlimit.Limiter, user store.User, scope, source string, term control.Term, cmdline string, stdin io.Reader, stdout, stderr io.Writer, done, stopping <-chan struct{}) int`

- [ ] **Step 1: Write the failing test**

In `internal/sshd/refusal_test.go`, update `TestRefusedPushIsAudited`'s
call to `Exec(cfg, st, nil, bob, …)` and add (imports gain `time` and
`gitbay.org/gitbay/internal/packlimit`):

```go
func TestCloneRefusedWhenPackSlotsAreFull(t *testing.T) {
	cfg, st, bob := execFixture(t)
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	var out, errOut bytes.Buffer
	code := Exec(cfg, st, packs, bob, "full", "SHA256:test", control.Term{}, "git-upload-pack alice/app",
		strings.NewReader(""), &out, &errOut, nil, nil)
	if code != protocol.ExitFailure || !strings.Contains(errOut.String(), "busy") {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/sshd -run TestCloneRefusedWhenPackSlotsAreFull -count=1`
Expected: build failure, too many arguments to `Exec`.

- [ ] **Step 3: Implement `Transport(ctx, …)`**

```go
func Transport(ctx context.Context, service, repoPath string, stdin io.Reader, stdout, errW io.Writer, extraEnv []string, maxPack int64) error {
	// (argument building unchanged)
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = errW
	return cmd.Run()
}
```

The doc comment gains: "ctx ending kills git."

- [ ] **Step 4: Implement in sshd**

`Server` gains `packs *packlimit.Limiter`; `New`:

```go
func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) (*Server, error) {
	s := &Server{cfg: cfg, st: st, packs: packs, authLimiter: newRateLimiter(cfg.Limits.SSHAuthRate, time.Minute), conns: map[*conn]struct{}{}, stopping: make(chan struct{})}
```

`runExec` passes it: `return Exec(s.cfg, s.st, s.packs, user, …, done, s.stopping)`.

`Exec` takes `packs` after `st` and passes `packs, done, stopping`
to `runGit`:

```go
			code := runGit(cfg, st, packs, user, scope, argv, stdin, stdout, stderr, done, stopping)
```

`runGit` signature:

```go
func runGit(cfg config.Config, st *store.Store, packs *packlimit.Limiter, user store.User, scope string, argv []string,
	stdin io.Reader, stdout, stderr io.Writer, done, stopping <-chan struct{}) int {
```

and after the pull-mirror refusal (line 439), before `dir :=`:

```go
	// Pack generation shares one budget with smart HTTP and git://.
	// receive-pack stays outside it: its post-receive runs after the
	// client has its report, and must not be queued or killed.
	ctx := context.Background()
	if !write {
		release, err := packs.Acquire(done, "user:"+strconv.FormatInt(user.ID, 10))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return protocol.ExitFailure
		}
		defer release()
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-done:
				// done closes on a restart too; a clone already running
				// finishes then. Only a departed client ends it.
				select {
				case <-stopping:
				default:
					cancel()
				}
			case <-ctx.Done():
			}
		}()
	}
```

and the transport call becomes
`gitutil.Transport(ctx, service, dir, stdin, stdout, stderr, env, maxPack)`.

`internal/sshd/sshd.go` imports `gitbay.org/gitbay/internal/packlimit`.

`cmd/gitbayd/system.go:97`:

```go
			// Each forced command is its own process, so there is no
			// shared pack budget in system mode (see Admin, [limits]).
			code := sshd.Exec(cfg, st, nil, user, key.Scope, key.Fingerprint, control.ParseTerm(os.Getenv("GITBAY_TERM")), cmdline, os.Stdin, os.Stdout, os.Stderr, nil, nil)
```

`internal/sshd/sshd_test.go:65`: `srv, err := New(cfg, st, nil)`.

`cmd/gitbayd/main.go:208`: `srv, err := sshd.New(cfg, st, nil)`, so
this commit builds; Task 6.5 passes the shared limiter.

- [ ] **Step 5: Run the packages**

Run: `go build ./... && go vet ./... && go test ./internal/sshd ./internal/gitutil -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gitutil internal/sshd cmd/gitbayd/system.go cmd/gitbayd/main.go
git commit -S -m "sshd: upload-pack and upload-archive take a pack slot; killed when the client leaves

Ref #262"
```

### Task 6.4: smart HTTP and git:// acquire a slot; ls-refs does not

**Files:**
- Modify: `internal/httpd/smart.go:25-38` (`Server.packs`, `New`), `:122-146` (`uploadPack`)
- Create: `internal/httpd/packlimit_test.go`
- Modify: `internal/gitd/gitd.go:22-27` (`Server.packs`, `New`), `:64-77` (`handle`)
- Create: `internal/gitd/gitd_test.go`

**Interfaces:**
- Consumes: `packlimit` (Task 6.1).
- Produces: `func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) *Server` in both `httpd` and `gitd`; unexported `lsRefs(br *bufio.Reader) bool` in `httpd`.

- [ ] **Step 1: Write the failing tests**

`internal/httpd/packlimit_test.go`:

```go
package httpd

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

func busyServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
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
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hold)
	var cfg config.Config
	cfg.Server.Root = t.TempDir()
	return &Server{cfg: cfg, st: st, packs: packs, stopping: make(chan struct{})}
}

func post(s *Server, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/alice/app/git-upload-pack", strings.NewReader(body))
	r.SetPathValue("owner", "alice")
	r.SetPathValue("repo", "app")
	w := httptest.NewRecorder()
	s.uploadPack(w, r)
	return w
}

func TestUploadPackBusyIs503(t *testing.T) {
	w := post(busyServer(t), "0000")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

// A protocol v2 ref listing generates no pack and is never queued.
func TestLsRefsBypassesTheLimit(t *testing.T) {
	w := post(busyServer(t), "0014command=ls-refs\n0000")
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("ls-refs was held to the pack limit")
	}
}
```

The repository directory does not exist, so the ls-refs request's git
exits non-zero; the test asserts only that it was not refused.

`internal/gitd/gitd_test.go`:

```go
package gitd

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

func TestBusyAnswersERR(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRepoSettings(repoID, func(rs *store.RepoSettings) { rs.GitDaemon = true }); err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, _ := packs.Acquire(nil, "ip:elsewhere")
	defer hold()

	s := New(config.Config{Server: config.Server{Root: t.TempDir()}}, st, packs)
	client, server := net.Pipe()
	defer client.Close()
	go s.handle(server)
	req := "git-upload-pack /alice/app.git\x00host=x\x00"
	fmt.Fprintf(client, "%04x%s", len(req)+4, req)
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readPktLine(client)
	if err != nil || !strings.HasPrefix(line, "ERR ") || !strings.Contains(line, "busy") {
		t.Fatalf("got %q, %v", line, err)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/httpd -run 'TestUploadPackBusyIs503|TestLsRefsBypassesTheLimit' -count=1; go test ./internal/gitd -run TestBusyAnswersERR -count=1`
Expected: build failures, `unknown field packs`.

- [ ] **Step 3: Implement in httpd**

`Server` gains `packs *packlimit.Limiter`; `New`:

```go
func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) *Server {
	proxies, _ := cfg.HTTP.TrustedProxyNets() // validated at config load
	return &Server{cfg: cfg, st: st, packs: packs, apiLimit: newAPILimiter(cfg.Limits.APIRate), proxies: proxies,
		stopping: make(chan struct{})}
}
```

`uploadPack`, from the gzip block to the end:

```go
	body := io.Reader(r.Body)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "bad gzip body", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}
	br := bufio.NewReader(body)
	if !lsRefs(br) {
		// Waiting ends when the client leaves or the daemon stops, so a
		// queued clone does not hold up a restart's drain.
		release, err := s.packs.Acquire(s.until(r), "ip:"+s.clientIP(r))
		if err != nil {
			if errors.Is(err, packlimit.ErrBusy) {
				w.Header().Set("Retry-After", "30")
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
			}
			return
		}
		defer release()
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	cmd := exec.CommandContext(r.Context(), toolpath.Look("git"), "upload-pack", "--stateless-rpc", dir)
	cmd.Env = append(os.Environ(), gitProtocolEnv(r)...)
	cmd.Stdin = br
	cmd.Stdout = w
	cmd.Run()
}

// lsRefs reports whether a protocol v2 request is a ref listing, which
// generates no pack. Its first pkt-line is "command=ls-refs".
func lsRefs(br *bufio.Reader) bool {
	const want = "command=ls-refs"
	head, err := br.Peek(4 + len(want))
	return err == nil && string(head[4:]) == want
}
```

Imports gain `bufio`, `errors`, and
`gitbay.org/gitbay/internal/packlimit`.

- [ ] **Step 4: Implement in gitd**

```go
type Server struct {
	cfg   config.Config
	st    *store.Store
	packs *packlimit.Limiter
}

func New(cfg config.Config, st *store.Store, packs *packlimit.Limiter) *Server {
	return &Server{cfg: cfg, st: st, packs: packs}
}
```

In `handle`, after the "repository not exported" check:

```go
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	release, err := s.packs.Acquire(nil, "ip:"+host)
	if err != nil {
		writeErr(conn, err.Error())
		return
	}
	defer release()
```

`net.Pipe`'s address is `"pipe"`, which `SplitHostPort` rejects; `host`
is then empty and the principal is `"ip:"`, which is fine for the test.

`cmd/gitbayd/main.go:225` and `:313`: `httpd.New(cfg, st, nil)` and
`gitd.New(cfg, st, nil)`, so this commit builds; Task 6.5 passes the
shared limiter.

- [ ] **Step 5: Run the packages**

Run: `go build ./... && go vet ./... && go test ./internal/httpd ./internal/gitd -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd internal/gitd cmd/gitbayd/main.go
git commit -S -m "httpd, gitd: pack generation takes a slot; ls-refs does not

Ref #262"
```

### Task 6.5: one limiter for the daemon; benchmark script; docs

**Files:**
- Modify: `cmd/gitbayd/main.go` (before `sshd.New`, line 204-208; `httpd.New`, line 225; `gitd.New`, line 313)
- Create: `deploy/clonebench.sh`
- Modify: `.gitbay/wiki/Admin.org` (`** [limits]`), `.gitbay/wiki/Performance.org`,
  `.gitbay/wiki/Architecture/09-Controls.org`, `.gitbay/wiki/Architecture/10-Known-Gaps.org`

**Interfaces:**
- Consumes: `config.Limits.PackLimits()` (Task 6.2), `packlimit.New` (Task 6.1), the three `New` signatures (Tasks 6.3, 6.4).

- [ ] **Step 1: Wire the limiter**

Before `errCh := make(chan error, 3)`:

```go
			// One pack-generation budget for SSH, smart HTTP and git://.
			packs := packlimit.New(cfg.Limits.PackLimits())
```

then `sshd.New(cfg, st, packs)`, `httpd.New(cfg, st, packs)`,
`gitd.New(cfg, st, packs).Serve(gln)`. Import
`gitbay.org/gitbay/internal/packlimit`.

- [ ] **Step 2: Build, vet, touched packages**

Run: `go build ./... && go vet ./... && go test ./cmd/gitbayd ./internal/sshd ./internal/httpd ./internal/gitd ./internal/packlimit ./internal/config ./internal/gitutil -count=1`
Expected: PASS.

- [ ] **Step 3: Benchmark script**

`deploy/clonebench.sh` (mode 0755):

```sh
#!/bin/sh
# clonebench.sh <clone-url> <n>: start n full bare clones of <clone-url>
# at once and print each one's wall time and outcome, then the total.
# Run from a machine other than the server, against a public repository.
set -eu
url=$1
n=$2
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
start=$(date +%s)
i=1
while [ "$i" -le "$n" ]; do
    (
        s=$(date +%s)
        if git clone --quiet --bare "$url" "$dir/$i.git" 2>"$dir/$i.err"; then
            echo "$i ok $(( $(date +%s) - s ))s"
        else
            echo "$i failed $(( $(date +%s) - s ))s: $(head -n 1 "$dir/$i.err")"
        fi
    ) &
    i=$((i + 1))
done
wait
echo "total $(( $(date +%s) - start ))s for $n clones"
```

Run: `sh -n deploy/clonebench.sh`
Expected: no output (syntax ok).

- [ ] **Step 4: Docs**

`Admin.org`, `** [limits]`, add after the `max_bytes_per_user` bullet:

```org
- =pack_concurrency= (3), =pack_per_principal= (2), =pack_queue= (32),
  =pack_queue_wait= (="60s"=) — git pack generation (clones, fetches,
  =git archive --remote=) over SSH, smart HTTP and git:// shares one
  budget: this many at once, this many per account (per client
  address when anonymous), and this many waiting for at most the wait.
  Past that an SSH client gets "the server is busy…" and exit 1, HTTP
  gets 503 with =Retry-After: 30=, git:// an =ERR= line. A queued
  client that disconnects leaves the queue; a running clone whose
  client disconnects is killed. Ref listings (info/refs, protocol v2
  =ls-refs=), pushes and web archives are outside the budget. For the
  three counts 0 means the default and a negative value turns that
  bound off. The defaults suit a four-core host; see [[Performance]].
  With =ssh.mode = "system"= each SSH session is its own process and
  SSH clones are not counted.
```

`Performance.org`, the last paragraph of `* Why it holds` becomes:

```org
The practical ceiling on this hardware is concurrent pack generation:
full clones of large repositories are CPU-bound in git itself (the 17s
clone ran git at ~156% CPU). =limits.pack_concurrency= bounds how many
run at once across SSH, HTTP and git://, with a queue behind it (see
[[Admin]], =[limits]=); the measurements below set its default.
```

and a new section at the end:

```org
* Concurrent clones

Measured with =deploy/clonebench.sh https://gitbay.org/krz/gitbay.git <n>=
from a machine outside bay1 (four cores), before and after the pack
limit was deployed with its defaults (=pack_concurrency= 3,
=pack_per_principal= 2, =pack_queue= 32, =pack_queue_wait= 60s). All
clones in one run come from one address, so the per-principal cap
applies to them; the "limit off" run sets the counts to -1.
```

The table itself is added by the operator from the runbook's #262
measurements, in the follow-up wiki MR described there.

`Architecture/09-Controls.org`, the concurrency row:

```org
| Concurrency limit on git pack generation    | in place | global, per-principal, bounded queue across SSH, HTTP and git:// (=internal/packlimit=); not in system SSH mode |
```

`Architecture/10-Known-Gaps.org`: delete the `#262` row. The question
row "How many concurrent clones does the host sustain?" stays until the
runbook's numbers are on the Performance page.

- [ ] **Step 5: Commit, MR, merge**

```bash
chmod 0755 deploy/clonebench.sh
git add cmd/gitbayd/main.go deploy/clonebench.sh .gitbay/wiki
git commit -S -m "gitbayd: one pack-generation limit for SSH, HTTP and git://

Closes #262"
git push -u origin pack-limit
gitbay mr create --source pack-limit --target main --title "git: limit concurrent pack generation across HTTP and SSH"
```

Run the runbook's #262 "before" measurement against production before
deploying this MR. Merge `--strategy ff` after CI, delete the branch
both places.

---

# Runbook for the operator (cmc)

The classifier refuses root ssh to bay1 from an assistant session; these
steps are run by hand. Operator ssh is `ssh -p 2222 root@gitbay.org`.

1. **Before merging MR 2 (#280).** `grep -A6 '^\[mail\]' /etc/gitbay/config.toml`
   on bay1. If `smtp_host` is not `localhost`/loopback, check the relay
   offers STARTTLS: `openssl s_client -starttls smtp -connect <smtp_host> -brief </dev/null`
   must complete a handshake. If it does not, either set
   `require_tls = false` in `[mail]` before deploying (and record why
   on the Admin page), or switch to the relay's implicit-TLS port with
   `tls = "implicit"`. After deploying, `gitbay dashboard --json | jq .queues`
   shows no mail failures after the next notification.
2. **Before merging MR 3 (#279).** `git --version` on bay1 must be
   2.37 or later (`http.curloptResolve`). If not, upgrade git first;
   mirrors fail with an unknown-config error otherwise. After
   deploying, `gitbay repo mirror sync krz/gitbay` then
   `gitbay repo mirror list krz/gitbay` shows the GitHub push mirror
   with no error.
3. **Deploying MR 4 (#282).** Check `gitbay build list` and the
   journal for a push in progress, then `make deploy`. After it:
   `stat -c '%a %U' /var/lib/gitbay/hook.sock` shows `600 gitbay`;
   push a commit to a scratch repository and confirm it lands and its
   push event appears in `gitbay feed`.
4. **After deploying MR 5 (#275).** On bay1, as the gitbay user:
   `sudo -u gitbay gitbayd --config /etc/gitbay/config.toml admin audit verify`
   prints "chain intact" with the unchained count equal to the rows
   written before the upgrade. `journalctl -u gitbayd -g 'msg=audit' -n 5`
   shows the rows the verify run's own session produced. Record the
   printed last hash somewhere off the host (a note in the
   password manager) if a manual anchor is wanted.
5. **MR 6 (#262) benchmark.** From the laptop, before deploying MR 6:
   `for n in 1 2 4 8; do sh deploy/clonebench.sh https://gitbay.org/krz/gitbay.git $n; done`,
   and on bay1 `uptime` during the n=8 run. After deploying MR 6
   (defaults), repeat, and additionally n=16 and n=40 (40 exceeds
   concurrency + queue from one address with per-principal 2 and shows
   the refusals). Record per n: total wall time, slowest clone,
   refused count, load average. Put the table under
   `* Concurrent clones` in `.gitbay/wiki/Performance.org` on a branch
   `wiki-clone-benchmark`, remove the "How many concurrent clones"
   question row from `Architecture/10-Known-Gaps.org`, and open an MR
   with `Ref #262`. If the numbers show the web staying slow with 3
   concurrent clones, lower `DefaultPackConcurrency` in the same MR
   and say so on the Admin page.
6. **After MR 1 (#281).** `openssl s_client -connect gitbay.org:443 -tls1_1 </dev/null`
   fails; `-tls1_2` and `-tls1_3` succeed.

# Release notes for whoever tags these

- mail: `mail.require_tls` defaults on for non-local relays; a relay
  without STARTTLS stops receiving mail unless `require_tls = false`.
  New `mail.tls = "implicit"` for port 465.
- mirror: git ≥ 2.37 required on the server; mirrors no longer follow
  redirects.
- hookd: pushes in flight across the upgrade lose their post-receive
  effects; deploy with none running.
- audit: schema 0070 adds the chain; rows before it are reported as
  unchained by `gitbayd admin audit verify`.
- limits: new `pack_*` settings with non-zero defaults; a burst of
  clones now queues and, past the queue, is refused with 503 / exit 1.

# Decisions and remaining questions

Decided 2026-09-28:

- **receive-pack stays outside the pack limit.**
- **bay1's relay and git**, checked: git 2.47.3 (`http.curloptResolve`
  needs 2.37); relay is AWS mail manager on port 587 and negotiates
  STARTTLS (TLS 1.3, certificate verified). MRs 2 and 3 are not blocked;
  runbook steps 1 and 2 stay as the check for other operators.

Remaining:

1. **System SSH mode.** With `ssh.mode = "system"` every session is a
   separate `gitbayd shell` process, so (a) the pack limiter cannot
   count SSH clones across sessions — this plan passes `nil` and says
   so on the Admin page — and (b) audit rows written there are not
   copied to the journal, because that process's stderr is the SSH
   client. The same applies to host `gitbayd admin …` commands. gitbay.org
   runs embedded mode; the plan documents the limit and adds nothing
   for system mode.
2. **Default pack limits.** 3 / 2 / 32 / 60s are an estimate from the
   one measured full clone (~1.5 cores). The runbook's benchmark
   decides whether they stand.

# Self-review

- Coverage against the issues: #281 MinVersion + Admin (MR 1). #280
  require_tls default by locality, implicit TLS (MR 2). #279 resolve
  and check before each sync, pin for git, http(s) only per
  `ValidateURL` (MR 3). #282 chmod 0600, SO_PEERCRED behind build
  tags, per-push token minted in `runGit` and required by hookd, works
  in both SSH modes through SQLite (MR 4). #275 refusals audited with
  per-actor limit, hash chain with `actor_ref`, `gitbayd admin audit
  verify`, journal copy from the daemon (MR 5). #262 global and
  per-principal limit, bounded queue, cancellation while queued (done /
  request context / stop) and while running (SSH ctx, HTTP request
  context), ls-refs and info/refs outside, knobs with 4-core defaults,
  benchmark script and Performance section, results via runbook (MR 6).
- Signatures used across tasks: `packlimit.New(max, per, queue int, wait time.Duration)`
  and `Limits.PackLimits() (max, per, queue int, wait time.Duration)`
  match; `Acquire(done <-chan struct{}, principal string)` is called
  with `done` (SSH), `s.until(r)` (HTTP), `nil` (git://, tests).
  `Exec` gains `packs` in MR 6 only; MR 5's test call is updated in
  Task 6.3 Step 1. `CreatePushToken` returns the raw token and
  `DeletePushToken` takes the raw token in both sshd and tests.
- Migrations 0069/0070 are within plan 3's range; renumber if another
  plan has taken them by then.
- No new route, template, ReadOnly command, control command or stdin
  reader, so no registry rows.
