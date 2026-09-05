# Browser login without an SSH key — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a person with no SSH key log into the web UI by requesting a
one-time link at their verified email address.

**Architecture:** An anonymous `POST /login` calls a plain exported function in
`internal/control`, which resolves the identifier to a user, mints the same
one-time token `web login` already mints, and mails it. The existing
`GET /login?token=` handler consumes it unchanged. No new control command,
because the caller has no authenticated user to run one as.

**Tech Stack:** Go, SQLite (hand-written SQL, no ORM), `html/template`,
`internal/mail` over SMTP.

**Spec:** `docs/specs/2026-09-04-email-login-design.md`

## Global Constraints

- Branch `email-login`. Never push to `main`. One MR, `Closes #155`.
- Never attribute anything to an assistant or model, anywhere.
- No ORM. Hand-written SQL. Migrations in `internal/store/migrations/`.
- Comments state facts, not before/after commentary. Git history holds the rest.
- `--json` output is the contract; human output is not.
- Locally run build, vet, and the unit tests of touched packages plus the one
  e2e test being written. Full e2e belongs to CI on bay1.
- Adding a top-level route means adding the word to `internal/policy/names.go`.
  `/login` already exists, so this plan adds no reserved name.

## Change from the spec

The spec proposed `RequestLoginLink` returning `(msg, errMsg string, code int)`
to mirror `control.RegisterAccount`. **It returns only `error` instead.**
`RegisterAccount` reports per-case failures because registration is allowed to
say what went wrong; here, reporting anything about the outcome is precisely the
enumeration leak the spec forbids. The returned error is for the server log
only — SMTP down, database failure — and is never rendered. The handler draws
the same page whatever it gets back.

## File Structure

| Path | Responsibility |
|---|---|
| `internal/store/migrations/0040_login_token_index.{up,down}.sql` | index behind the throttle count |
| `internal/store/sessions.go` | `CountLoginTokensSince`, beside `CreateLoginToken` |
| `internal/control/loginlink.go` | new — resolve identifier, throttle, mint, mail |
| `internal/httpd/accounts.go` | `loginSubmit`; session cookie `SameSite` |
| `internal/httpd/routes.go` | `POST /login` |
| `internal/web/templates/login.html` | request form and the sent confirmation |
| `internal/store/sessions_test.go` | new — counter unit test |
| `internal/httpd/logincookie_test.go` | new — cookie attribute test |
| `e2e/emaillogin_test.go` | new — the whole path against real SMTP |

---

### Task 1: The throttle counter and its index

`login_tokens` has no index on `user_id`, and rows are never deleted. Counting
per user on an anonymous endpoint would be an unbounded table scan on every
request, which makes the throttle its own denial-of-service vector.

**Files:**
- Create: `internal/store/migrations/0040_login_token_index.up.sql`
- Create: `internal/store/migrations/0040_login_token_index.down.sql`
- Modify: `internal/store/sessions.go` (append after `CreateLoginToken`, line 36)
- Create: `internal/store/sessions_test.go`

**Interfaces:**
- Consumes: `Store.CreateLoginToken(userID int64, hash string, ttl time.Duration) error`, `store.NewToken() (token, hash string, err error)` — both exist.
- Produces: `func (s *Store) CountLoginTokensSince(userID int64, since time.Time) (int, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/store/sessions_test.go`:

```go
package store

import (
	"testing"
	"time"
)

func TestCountLoginTokensSince(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateLoginToken(uid, hash, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.CountLoginTokensSince(uid, time.Now().Add(-time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("count in the last hour = %d, %v; want 3", n, err)
	}

	// A window that opens in the future sees none of them, which is what
	// makes the hourly bound a window rather than a lifetime total.
	if n, err := s.CountLoginTokensSince(uid, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("count in a future window = %d, %v; want 0", n, err)
	}

	// One account's requests must not spend another account's budget.
	other, err := s.CreateUser("kim", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountLoginTokensSince(other, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("other account count = %d, %v; want 0", n, err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/store/ -run TestCountLoginTokensSince -v`
Expected: FAIL — `s.CountLoginTokensSince undefined`.

- [ ] **Step 3: Write the migration**

`internal/store/migrations/0040_login_token_index.up.sql`:

```sql
CREATE INDEX login_tokens_user_created ON login_tokens(user_id, created_at);
```

`internal/store/migrations/0040_login_token_index.down.sql`:

```sql
DROP INDEX login_tokens_user_created;
```

- [ ] **Step 4: Write the counter**

Append to `internal/store/sessions.go`, directly after `CreateLoginToken`:

```go
// CountLoginTokensSince counts the login tokens minted for a user within a
// window. An unauthenticated request can ask for a login link, so the mint
// needs a durable per-account bound the way email verification does (#136).
func (s *Store) CountLoginTokensSince(userID int64, since time.Time) (int, error) {
	var n int
	err := s.DB.QueryRow(
		"SELECT count(*) FROM login_tokens WHERE user_id = ? AND created_at > ?",
		userID, fmtTime(since)).Scan(&n)
	return n, err
}
```

- [ ] **Step 5: Run the test and the package**

Run: `go test ./internal/store/ -run TestCountLoginTokensSince -v`
Expected: PASS.

Run: `go test ./internal/store/`
Expected: PASS — the new migration must not break existing store tests.

- [ ] **Step 6: Confirm the index is actually used**

Run:
```bash
cd /Users/cmc/git/krz/gitbay && cat > /tmp/plan_explain_test.go <<'EOF'
EOF
go test ./internal/store/ -run TestDashboardQueriesUseIndexes -v
```
Expected: PASS (unrelated, but proves the migration did not disturb existing
plans). Then verify by hand that the count uses the index:

```bash
sqlite3 "$(mktemp -d)/x.db" <<'EOF'
CREATE TABLE login_tokens (token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT);
CREATE INDEX login_tokens_user_created ON login_tokens(user_id, created_at);
EXPLAIN QUERY PLAN SELECT count(*) FROM login_tokens WHERE user_id = 1 AND created_at > 'x';
EOF
```
Expected: the plan names `login_tokens_user_created`, not `SCAN login_tokens`.

- [ ] **Step 7: Commit**

```bash
git add internal/store/sessions.go internal/store/sessions_test.go \
  internal/store/migrations/0040_login_token_index.up.sql \
  internal/store/migrations/0040_login_token_index.down.sql
git commit -m "store: count login tokens per account, indexed

Ref #155"
```

---

### Task 2: Session cookie SameSite

A link clicked in a webmail client is a cross-site top-level navigation. Under
`SameSite=Strict` the redirect chain to `/` can arrive without the cookie, so
the visitor lands logged out and is logged in only after a refresh. Pasting a
URL into the address bar does not hit this, which is why the SSH flow never
showed it.

`Lax` is safe here, and it was verified rather than assumed: every
cookie-authenticated mutating route carries `checkOrigin`. The four POST routes
without it — `git-upload-pack`, `git-receive-pack`, `lfs/objects/batch`,
`/api/v1/cmd` — do not accept the session cookie at all; the API takes only
`Authorization: Bearer` (`internal/httpd/api.go:126`). For POST, `Lax` is
strictly stronger than the Origin check, because it withholds the cookie
outright. The only `Mutating: true` GET is `/login` itself, whose token is
single-use.

**Files:**
- Modify: `internal/httpd/accounts.go:92` (set), and the `clearCookie` call in `logout`
- Create: `internal/httpd/logincookie_test.go`

**Interfaces:**
- Consumes: `Server.clearCookie(name string, sameSite http.SameSite) *http.Cookie` (`internal/httpd/flash.go:55`), `sessionCookie`
- Produces: nothing new; changes an attribute value.

- [ ] **Step 1: Write the failing test**

Create `internal/httpd/logincookie_test.go`:

```go
package httpd

import (
	"net/http"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// The session cookie must be Lax, not Strict. A login link clicked in a mail
// client is a cross-site top-level navigation, and Strict can withhold the
// cookie through the redirect that follows, so the visitor lands logged out
// (#155). Cross-site POSTs stay protected: Lax withholds the cookie from them,
// and checkOrigin refuses them besides.
func TestSessionCookieIsLax(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	s.cfg.HTTP.TLS = "acme"
	if got := s.clearCookie(sessionCookie, sessionSameSite); got.SameSite != http.SameSiteLaxMode {
		t.Errorf("clearing cookie SameSite = %v, want Lax", got.SameSite)
	}
	if sessionSameSite != http.SameSiteLaxMode {
		t.Errorf("sessionSameSite = %v, want Lax", sessionSameSite)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/httpd/ -run TestSessionCookieIsLax -v`
Expected: FAIL — `undefined: sessionSameSite`.

- [ ] **Step 3: Introduce the constant and use it in both places**

In `internal/httpd/accounts.go`, near the `sessionCookie` declaration, add:

```go
// sessionSameSite is Lax so a login link followed from a mail client keeps
// its session through the redirect. Cross-site POSTs are refused by
// checkOrigin and carry no Lax cookie anyway.
const sessionSameSite = http.SameSiteLaxMode
```

In the `login` handler (`internal/httpd/accounts.go:92`), replace
`SameSite: http.SameSiteStrictMode,` with `SameSite: sessionSameSite,`.

In the `logout` handler, replace
`s.clearCookie(sessionCookie, http.SameSiteStrictMode)` with
`s.clearCookie(sessionCookie, sessionSameSite)` so the set and clear paths
match, which is the rule `TestClearCookieMirrorsTheSettingCall` exists to keep.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/httpd/`
Expected: PASS, including the pre-existing
`TestClearCookieMirrorsTheSettingCall`, which passes its own `SameSite`
argument and is unaffected.

- [ ] **Step 5: Commit**

```bash
git add internal/httpd/accounts.go internal/httpd/logincookie_test.go
git commit -m "httpd: session cookie is SameSite=Lax

A link followed from a mail client is a cross-site navigation; Strict can
drop the cookie through the redirect after /login?token=. Every
cookie-authenticated mutating route carries checkOrigin, and Lax withholds
the cookie from cross-site POSTs regardless.

Ref #155"
```

---

### Task 3: The login link request

The deliverable. The function, the route, the handler, and the form land
together because none of them is testable without the others.

**Files:**
- Create: `internal/control/loginlink.go`
- Modify: `internal/httpd/accounts.go` (`renderLogin`, new `loginSubmit`)
- Modify: `internal/httpd/routes.go:100` (add `POST /login`)
- Modify: `internal/web/templates/login.html`
- Create: `e2e/emaillogin_test.go`

**Interfaces:**
- Consumes: `store.UserIDByVerifiedEmail(address string) (int64, bool)` (`internal/store/activity.go:11`); `store.PrimaryVerifiedEmail(userID int64) (string, error)` (`internal/store/mrs.go:440`); `store.UserByName`; `store.CountLoginTokensSince` (Task 1); `store.NewToken`; `store.CreateLoginToken`; `mail.Send(cfg config.Config, to, subject, body string) error`; `Server.apiLimit.allow(key string, write bool) (bool, time.Duration)`; `Server.clientIP(r)`.
- Produces: `func control.RequestLoginLink(cfg config.Config, st *store.Store, identifier string) error`

- [ ] **Step 1: Write the failing e2e test**

Create `e2e/emaillogin_test.go`:

```go
package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// A person with no SSH key can still get into the web UI: they ask for a
// link by username or verified address and it arrives by mail (#155).
func TestEmailLogin(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))

	// No --key: this account has no way to authenticate over SSH at all,
	// which is the whole point.
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")

	browser := newBrowser(t)
	status, body := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"dana@example.test"}})
	if status != 200 {
		t.Fatalf("POST /login: %d", status)
	}
	if !strings.Contains(body, "on its way") {
		t.Fatalf("no confirmation in body: %s", body)
	}

	msg := smtp.waitFor(t, "dana@example.test", "/login?token=")
	i := strings.Index(msg, "/login?token=")
	link := msg[i:]
	if j := strings.IndexAny(link, " \r\n"); j >= 0 {
		link = link[:j]
	}

	if status, _ := browserGet(t, browser, inst.base()+link); status != 200 {
		t.Fatalf("following the link: %d", status)
	}
	status, body = browserGet(t, browser, inst.base()+"/settings")
	if status != 200 || !strings.Contains(body, "dana@example.test") {
		t.Fatalf("not logged in after the link: %d", status)
	}

	// The link is single use.
	second := newBrowser(t)
	browserGet(t, second, inst.base()+link)
	if status, _ := browserGet(t, second, inst.base()+"/settings"); status == 200 {
		t.Error("login link worked twice")
	}
}

// The response must not say whether an account exists. A different status,
// body, or destination answers "is this person here?" to anyone who asks.
func TestEmailLoginDoesNotEnumerate(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")
	// An account whose address was never verified must look like an absent
	// one, or an unverified address becomes an oracle.
	inst.admin(t, "admin", "user", "create", "eve", "--email", "eve@example.test")

	browser := newBrowser(t)
	real1, bodyReal := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"dana@example.test"}})
	absent, bodyAbsent := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"nobody@example.test"}})
	unver, bodyUnver := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"eve@example.test"}})
	empty, bodyEmpty := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {""}})

	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"absent", absent, bodyAbsent},
		{"unverified", unver, bodyUnver},
		{"empty", empty, bodyEmpty},
	} {
		if c.status != real1 || c.body != bodyReal {
			t.Errorf("%s differs from a real address: status %d vs %d", c.name, c.status, real1)
		}
	}
	if len(smtp.mailTo("eve@example.test")) != 0 {
		t.Error("mailed an unverified address")
	}
	if len(smtp.mailTo("nobody@example.test")) != 0 {
		t.Error("mailed an address with no account")
	}
}

// An anonymous endpoint that sends mail needs a durable per-account bound,
// the same one email verification has (#136).
func TestEmailLoginThrottled(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")

	browser := newBrowser(t)
	for i := 0; i < 6; i++ {
		browserPost(t, browser, inst.base()+"/login",
			url.Values{"identifier": {"dana@example.test"}})
	}
	if n := len(smtp.mailTo("dana@example.test")); n > 5 {
		t.Fatalf("sent %d login mails in an hour, want at most 5", n)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./e2e/ -run TestEmailLogin -v -timeout 20m`
Expected: FAIL — `POST /login: 405`, because no such route exists.

Note the explicit `-timeout`: `go test` defaults to 10 minutes and the e2e
suite has exceeded it before, which reads as a hang rather than a failure
(#143).

- [ ] **Step 3: Write the control function**

Create `internal/control/loginlink.go`:

```go
package control

import (
	"fmt"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/store"
)

// maxLoginLinksPerHour bounds what one account's address can be made to
// receive. It matches maxEmailAddsPerHour: enough for a person who mistypes
// and retries, nothing for a script.
const maxLoginLinksPerHour = 5

// loginLinkTTL is longer than the five minutes an SSH-minted link gets.
// That one is pasted from a terminal already open; this one has to survive
// delivery and someone noticing the mail.
const loginLinkTTL = 15 * time.Minute

// RequestLoginLink mails a one-time login link to the account named by
// identifier, which is a username or a verified email address.
//
// It is not a registered command: the caller is an unauthenticated web
// request, and commands run as c.User. RegisterAccount is exported for the
// same reason.
//
// The returned error is for the server log only. Nothing about the outcome
// may reach the caller — that a request found an account, found one without
// a verified address, or found nothing at all must be indistinguishable, or
// the endpoint answers "does this person have an account here?" to anyone
// who asks. Every miss returns nil.
func RequestLoginLink(cfg config.Config, st *store.Store, identifier string) error {
	if cfg.Web.Mode != "accounts" || cfg.Mail.SMTPHost == "" {
		return nil
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil
	}

	var userID int64
	var address string
	if strings.Contains(identifier, "@") {
		id, ok := st.UserIDByVerifiedEmail(identifier)
		if !ok {
			return nil
		}
		userID, address = id, identifier
	} else {
		u, err := st.UserByName(identifier)
		if err != nil {
			return nil
		}
		addr, err := st.PrimaryVerifiedEmail(u.ID)
		if err != nil || addr == "" {
			return nil
		}
		userID, address = u.ID, addr
	}

	n, err := st.CountLoginTokensSince(userID, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= maxLoginLinksPerHour {
		return nil
	}

	token, hash, err := store.NewToken()
	if err != nil {
		return err
	}
	if err := st.CreateLoginToken(userID, hash, loginLinkTTL); err != nil {
		return err
	}
	host := siteHost(cfg)
	body := fmt.Sprintf(
		"Someone (hopefully you) asked to log in to %s.\n\n"+
			"Open this link within 15 minutes. It works once:\n\n    %s/login?token=%s\n\n"+
			"If this wasn't you, ignore this mail. Nothing has changed on the account.\n",
		host, strings.TrimSuffix(cfg.Server.SiteURL, "/"), token)
	return mail.Send(cfg, address, "log in to "+host, body)
}
```

Check `st.UserByName`'s real name and signature before writing this — if the
store spells it differently, use the store's spelling rather than adding a
wrapper. Run: `grep -n "func (s \*Store) UserByName" internal/store/users.go`

- [ ] **Step 4: Write the handler**

In `internal/httpd/accounts.go`, replace `renderLogin` and add `loginSubmit`:

```go
// renderLogin draws the login page. Mode carries the registration mode so
// the page can tell a brand-new visitor how to get an account. EmailLogin
// says whether this instance can mail a link; Sent switches the page to the
// confirmation that follows a request.
func (s *Server) renderLogin(w http.ResponseWriter, errMsg string, sent bool) {
	s.render(w, "login.html", struct {
		basePage
		Mode       string // closed | invite | open
		Error      string
		EmailLogin bool
		Sent       bool
	}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()},
		s.cfg.Registration.Mode, errMsg, s.emailLoginEnabled(), sent})
}

// emailLoginEnabled reports whether a link can be mailed at all. There is no
// separate switch: the capability is exactly the SMTP the instance already
// configured for verification and notification mail.
func (s *Server) emailLoginEnabled() bool {
	return s.cfg.Web.Mode == "accounts" && s.cfg.Mail.SMTPHost != ""
}

// loginSubmit mails a one-time login link. The response is the same page
// whatever happened, including when nothing happened.
func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.emailLoginEnabled() {
		s.notFound(w, r)
		return
	}
	// The per-account bound lives in the store and survives a restart; this
	// one stops a single source from spending every account's budget.
	if allowed, wait := s.apiLimit.allow("login"+s.clientIP(r), true); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many login requests; wait a moment", http.StatusTooManyRequests)
		return
	}
	if err := control.RequestLoginLink(s.cfg, s.st, r.FormValue("identifier")); err != nil {
		log.Printf("login link: %v", err)
	}
	s.renderLogin(w, "", true)
}
```

Update the two existing `renderLogin` calls in the `login` handler to pass
`false` as the new argument.

Add `"log"` and `"strconv"` to the file's imports if they are not already
there, and `"gitbay.org/gitbay/internal/control"` if absent.

- [ ] **Step 5: Add the route**

In `internal/httpd/routes.go`, directly after the `GET /login` line at 100:

```go
			Route{Method: "POST", Pattern: "/login", Mutating: true,
				Handler: s.checkOrigin(s.loginSubmit)},
```

- [ ] **Step 6: Update the template**

Replace the top of `internal/web/templates/login.html`, keeping the "New
here?" block below it exactly as it is:

```html
{{define "title"}}login · {{.Site}}{{end}}
{{define "content"}}
<h1>Log in</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
{{if .Sent}}
<p>If that account exists, a login link is on its way. It works once and
expires in fifteen minutes.</p>
{{else}}
{{if .EmailLogin}}
<form method="post" action="/login">
  <label for="identifier">Username or email address</label>
  <input type="text" id="identifier" name="identifier" autocomplete="username" required>
  <button type="submit">Email me a link</button>
</form>
<p>Or, from a machine with your registered key:</p>
{{else}}
<p>Browser sessions are minted over SSH — there is no password. From a machine
with your registered key:</p>
{{end}}
<pre class="message">ssh git@{{.Host}} web login</pre>
<p>then open the printed URL within five minutes.</p>
{{end}}
```

The `<label for>` is not decoration: `Web:InputWithoutLabelCheck` and #133
cover this, and a placeholder is not an accessible name.

- [ ] **Step 7: Build, vet, and run the e2e tests**

```bash
go build ./... && go vet ./...
go test ./internal/httpd/ ./internal/control/ ./internal/store/
go test ./e2e/ -run TestEmailLogin -v -timeout 20m
```
Expected: all PASS, including `TestEveryCommandIsReachable` and
`TestViewOnlyHasNoMutatingRoutes` in their packages.

- [ ] **Step 8: Commit**

```bash
git add internal/control/loginlink.go internal/httpd/accounts.go \
  internal/httpd/routes.go internal/web/templates/login.html \
  e2e/emaillogin_test.go
git commit -m "web: request a login link by email

An account with no SSH key had no way into the web UI at all: the only
caller of CreateWebSession consumed a token that only 'web login' over SSH
could mint. An unauthenticated POST /login now mails the same one-time
token, throttled per account and per source, with a response that does not
vary with whether the account exists.

Closes #155"
```

---

### Task 4: Parity row and the merge request

The Parity wiki page is a maintained matrix of capability by surface, and the
convention is to update the row in the change that moves it.

**Files:**
- Modify: `Parity.org` in the `krz/gitbay.wiki` clone

- [ ] **Step 1: Clone or update the wiki**

```bash
cd /tmp && git clone ssh://git@gitbay.org/krz/gitbay.wiki 2>/dev/null || \
  (cd /tmp/gitbay.wiki && git pull)
```

- [ ] **Step 2: Add the row**

Open `/tmp/gitbay.wiki/Parity.org`, find the table that carries the
authentication and account rows, and add a row for browser login in the same
format the neighbouring rows use: available on web, not applicable to CLI or
SSH (SSH has `web login`, which is the row above). Match the file's existing
markers rather than inventing new ones — read three neighbouring rows first.

- [ ] **Step 3: Commit and push the wiki**

```bash
cd /tmp/gitbay.wiki
git add Parity.org
git commit -m "Parity: browser login by emailed link"
git push
```

- [ ] **Step 4: Push the branch and open the merge request**

```bash
cd /Users/cmc/git/krz/gitbay
git push -u origin email-login
gitbay mr create --source email-login --target main \
  --title "Browser login without an SSH key" --file - <<'EOF'
An account with no SSH key could not use the web UI at all. `CreateWebSession`
has one caller, the `/login?token=` handler, and only `web login` over SSH could
mint a token for it.

An unauthenticated `POST /login` now mails the same one-time token to a verified
address, resolved by username or address. Bounded at five an hour per account in
the store and by the existing token bucket per source. The response does not
vary with whether the account exists, whether its address is verified, or
whether it is over its budget.

The session cookie moves from `SameSite=Strict` to `Lax`, because a link clicked
in a mail client is a cross-site navigation and Strict can drop the cookie
through the redirect. Every cookie-authenticated mutating route carries
`checkOrigin`; the POST routes that do not take only bearer tokens or no auth at
all.

This does not widen what a browser session can do. The web dispatches with
`ViaAPI: true`, so no `SSHOnly` command is reachable from one however it was
obtained.

Design: `docs/specs/2026-09-04-email-login-design.md`.
Plan: `docs/plans/2026-09-04-email-login.md`.

Closes #155
EOF
```

- [ ] **Step 5: Wait for CI, then merge**

```bash
gitbay build list --json
```

Poll no more often than every 120 seconds: the CLI shares one SSH connection
per instance, and the auth limiter reads a burst of failures as an attack.

When green:

```bash
gitbay mr merge <n> --strategy squash
git checkout main && git pull
git branch -d email-login && git push origin --delete email-login
```

---

## Self-Review

**Spec coverage.** Every spec section maps to a task: the exported function,
resolution, and mail body to Task 3 Step 3; TTL to `loginLinkTTL`; the durable
per-account throttle to Task 1 and its use in Task 3; the per-IP throttle to
Task 3 Step 4; enumeration to `TestEmailLoginDoesNotEnumerate`; the empty
identifier to the same test; the cookie change to Task 2; the no-new-config
decision to `emailLoginEnabled`; out-of-scope signup untouched. The spec's
implementation gate on `checkOrigin` coverage was discharged before this plan
was written and its result is recorded in Task 2.

**Deviation.** The return type of `RequestLoginLink` changed from the spec's
triple to a single `error`, recorded above under "Change from the spec".

**Types.** `RequestLoginLink(config.Config, *store.Store, string) error`;
`CountLoginTokensSince(int64, time.Time) (int, error)`;
`emailLoginEnabled() bool`; `renderLogin(http.ResponseWriter, string, bool)`.
Each is used with that signature everywhere it appears. `sessionSameSite` is
declared in Task 2 and used in Task 2 only.

**Unverified at plan time.** `store.UserByName` is used in Task 3 Step 3 but
its exact name and signature were not confirmed; Step 3 carries an explicit
instruction to check before writing. `Parity.org`'s table format is likewise
read at execution rather than guessed.
