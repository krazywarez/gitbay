# Browser login without an SSH key

Ref #155. Milestone v1.14.0. First half of the identity work; OIDC is the
second half and reuses the seam this lands.

## Problem

`store.CreateWebSession` has exactly one caller: the `/login?token=` handler at
`internal/httpd/accounts.go:89`. The token it consumes can only be minted by
`web login` over SSH (`internal/control/web.go:runWebLogin`). Web signup
requires a pasted SSH public key (`internal/httpd/accounts.go:211`).

A person without an SSH key therefore cannot use the web UI at all. That is not
a rough edge for non-engineers, it is a closed door. `admin user create` already
makes `--key` optional, so an admin can create an account today that has no way
to authenticate anywhere.

`web.password_auth` exists as a config field and is rejected at startup
(`internal/config/config.go:325`) with "not implemented yet".

## What this does not change

The web dispatches control commands with `ViaAPI: true`
(`internal/httpd/control.go:37`), and `control.Dispatch` refuses `SSHOnly`
commands under that flag (`internal/control/control.go:103`). Twenty-nine
commands carry `SSHOnly`: secrets, API token minting, mirror credentials, the
audit log, session revocation, and the whole `admin` family.

A browser session cannot reach any of them, whoever holds it and however it was
obtained. Adding a second way to get a session widens who may hold one, not what
one can do. A keyless account also cannot push, because writes go over SSH and
pushing requires a key by definition.

## Approach

Email a single-use login link. Rejected alternatives:

- **Password plus TOTP.** Stores a new secret at rest, needs a lockout policy,
  and its reset flow needs SMTP anyway — a superset of this design's
  dependencies rather than an alternative to them.
- **Both, config-gated.** Two auth paths to secure and test, for one user.

## Design

### The exported function

The mint must be triggerable by an unauthenticated request, so it cannot be a
registered control command: those run as `c.User` and there is none. The
precedent is `control.RegisterAccount`, a plain exported function that
`signupSubmit` calls for the same reason.

New file `internal/control/loginlink.go`:

```go
func RequestLoginLink(cfg config.Config, st *store.Store, identifier string) (msg, errMsg string, code int)
```

Same return triple as `RegisterAccount`. No registry entry, so
`TestEveryCommandIsReachable` is unaffected and no CLI passthrough is needed —
anyone at a terminal has SSH and already has `web login`.

Resolution: an identifier containing `@` goes to `store.UserIDByVerifiedEmail`;
otherwise look up the username and take `store.PrimaryVerifiedEmail`. Both
exist. Only verified addresses resolve; an unverified one is treated as no
match. An empty or whitespace-only identifier resolves to no match by the same
path, so it draws the same response as everything else.

The body follows `sendVerification` (`internal/control/register.go:41`) and
ends in `mail.Send`.

### TTL

`CreateLoginToken` already takes a TTL, so no signature changes. SSH-minted
links keep **5 minutes**. Emailed links get **15**, because delivery plus a
person noticing the mail does not fit in five.

### Throttling

Two layers.

- **Per account, durable.** New `store.CountLoginTokensSince(userID, since)`,
  capped at 5 per hour. This copies `maxEmailAddsPerHour` and its reasoning from
  #136, and survives a restart.
- **Per IP.** Reuse the existing `apiLimiter` token bucket
  (`internal/httpd/apilimit.go`) on the POST route. An anonymous endpoint that
  sends mail is a spam cannon without it.

### Enumeration

The response is identical whether the account exists, exists without a verified
address, or is over its throttle: "if that account exists, a link is on its
way." `RequestLoginLink` returns a generic `msg` and keeps the distinction
internal. Differences in status code, body, or redirect target all count as a
leak.

### Cookie SameSite

The session cookie is `SameSiteStrictMode` (`internal/httpd/accounts.go:92`).
Clicking a link in a webmail client is a cross-site top-level navigation, and
the redirect chain to `/` can arrive without the cookie: the visitor lands
logged out, refreshes, and is then logged in. Pasting a URL into the address bar
does not hit this, which is why the SSH flow has never shown it.

Change the session cookie to `SameSiteLaxMode`. Lax still withholds the cookie
from cross-site POSTs, and the Origin check on mutating routes
(`internal/httpd/accounts.go:55`) is the stronger of the two CSRF defenses.

**Implementation gate:** confirm that Origin check covers every mutating route
before relying on it. If it does not, extend it in this branch or keep Strict
and add an interstitial "Continue" page on `/login?token=` instead.

### Configuration

No new flag. The form renders when `cfg.Mail.SMTPHost != ""` and
`web.mode = "accounts"`. A `web.email_login` switch was considered and dropped
as unneeded.

### Out of scope

Web signup keeps requiring an SSH key. A keyless account arrives through
`admin user create <name> --email <address> --verified`, which works today with
no code change, and matches how a team adds a designer. Keyless self-signup is a
separate policy question that widens the open-registration spam surface.

## Files

| Path | Change |
|---|---|
| `internal/control/loginlink.go` | new — `RequestLoginLink` |
| `internal/store/sessions.go` | new — `CountLoginTokensSince`, beside `CreateLoginToken` |
| `internal/httpd/accounts.go` | `loginSubmit` handler; cookie `SameSite` |
| `internal/httpd/routes.go` | `POST /login` |
| `internal/web/templates/login.html` | the request form |
| `e2e/emaillogin_test.go` | new |

## Tests

- A verified address queues mail, and the token in it completes a session.
- A nonexistent identifier produces a byte-identical response to a real one.
- An address that exists but is unverified mints nothing.
- The sixth request within an hour mints nothing.
- An expired emailed token is refused (covered by `ConsumeLoginToken`).
- The session cookie asserts `Lax`, `HttpOnly`, and `Secure` under TLS.

## Phase 2

OIDC becomes another resolver in front of `CreateWebSession`, reusing the
session layer, the cookie decision, and the login page this adds.
