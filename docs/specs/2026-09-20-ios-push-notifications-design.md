# iOS push notifications

Closes #89. gitbayd delivers activity to registered Apple devices over
APNs, as a third route beside the inbox row and the activity mail that
`notify()` already sends.

## Problem

`krz/gitbay-ios` is a reading and reviewing surface for the times its
user is not at a keyboard, and it has no way to say anything happened.
`DESIGN.org` records the gap as "No push notifications — poll on
foreground, use background refresh; not planned, propose if the app
makes the case". This is that proposal.

Background refresh does not close the gap. `BGAppRefreshTask` is
opportunistic: the system runs it when it feels like it, routinely
fifteen minutes to hours after the event, and it cannot be relied on to
badge. A failed build is exactly the notice that is worthless late.

## Decision

gitbayd speaks APNs directly, over HTTP/2, authenticated by a JWT it
signs with an operator-supplied `.p8` key. Notices become queue rows and
a drainer sends them with the same bounded-retry discipline the mail
queue and the webhook deliverer already use.

Decisions taken on the way, with the alternatives rejected:

- **gitbay.org only.** An APNs key belongs to a bundle ID, and only the
  author of `org.gitbay.gitbay` holds one. A self-hoster gets push by
  shipping their own build under their own bundle ID and pointing
  `[push]` at their own key; the App Store build talks to gitbay.org.
  The config is written so that already works — nothing in the server
  hardcodes an instance.
- **No relay.** A service this instance operates, holding the key and
  accepting pushes from other instances, was the only way to give
  strangers push with the App Store build. At one-instance scope it is
  a second deployment to run and back up, and it would put other
  people's notification text through this server for no benefit anyone
  asked for. Declined. If self-hosters ever ask, the queue row is
  already the right unit to hand to one.
- **No new Go dependency.** APNs requires HTTP/2, and stdlib
  `net/http` negotiates h2 over ALPN. Token auth is an ES256 JWT over a
  fixed two-field header and three-field claim set — `crypto/ecdsa`
  and `encoding/json`, no JWT library. A provider token is valid an
  hour and must not be reminted faster than once per twenty minutes,
  so it is cached and refreshed at fifty.
- **Full text in the payload, private repositories included.** The
  alternative sends "new activity on krz/gitbay" and has the app fetch
  the detail, which needs a Notification Service Extension holding a
  bearer token in a shared keychain group. That is real complexity to
  keep a repository name off a lock screen on a single-user instance.
  The trade is recorded here rather than made configurable: a private
  repository's name, item number and summary reach Apple and appear on
  the lock screen. Revisit if the instance stops having one human user.
- **Device rows, not user rows.** A token identifies an install, and
  one account signs in from a phone and an iPad. Registration is
  per-device, keyed on the token.
- **Reaped by APNs, not by a job.** A `410 Unregistered` response, and
  a `400` whose reason is `BadDeviceToken`, delete the device row. No
  expiry sweep; Apple is authoritative about which tokens are live.
- **`issue assign` starts filing a notice.** #89 names assignments and
  assignment is not among the sixteen `notify()` call sites today — the
  dashboard surfaces assigned work and nothing announces it. Fixed
  here, because a push feature that is silent on the thing the issue
  asked for is not the feature. It lands as its own commit and is
  worth having with or without push.

## Data

Migration 0059. Two tables and one column.

```sql
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

ALTER TABLE users ADD COLUMN notify_push INTEGER NOT NULL DEFAULT 1;
```

The mail queue's table is named `notifications`, so the push queue
cannot be. `push_queue` mirrors its columns exactly, which is what lets
the drainer be a copy of the mailer's loop rather than a new design.

`notify_push` defaults to 1 and costs nothing when the account has no
devices: an account that never registers one is unaffected by the
column. It exists so a user with two devices can silence both without
deregistering each.

Rows are stored per device rather than per notice, so a retry to one
device does not resend to the other. A notice reaching a user with two
devices writes two rows.

Retention: `push_queue` joins the `[retention]` sweep beside the mail
queue, as a new `push` key on `config.Retention` and a corresponding
sweep in `internal/store/retention.go`.

## Config

```toml
[push]
enabled     = true
key_file    = "/etc/gitbay/apns.p8"
key_id      = "ABC123DEFG"
team_id     = "ZCNAX3VL9D"
topic       = "org.gitbay.gitbay"
environment = "production"   # or "sandbox"
```

`environment` picks the host: `api.push.apple.com` or
`api.sandbox.push.apple.com`. It is a named mode rather than a raw URL
so a typo cannot aim the key at a host that is not Apple's.

Validation at load, in the manner of `max_snippets_per_user`'s negative
check (#214): with `enabled = true`, the four string fields must be
non-empty, `environment` must be one of the two names, and `key_file`
must exist and parse as an EC private key. A misconfigured `[push]`
refuses to start rather than failing silently at the first notice —
the failure mode otherwise is a queue that fills and dead-letters with
nobody watching.

The `.p8` is read at startup and referenced by path, as `host_keys` and
the TLS `key_file` are. It is never in the repository, never in argv,
never logged. File mode 0600, owned by the account gitbayd runs as.

## Commands

The capability lands in the registry; the surfaces render it.

| Command | Notes |
|---|---|
| `notifications device add` | `--label <name>`, token on stdin. `ReadsStdin: true`. |
| `notifications device list` | `ReadOnly`. Token shown truncated, never in full. |
| `notifications device remove <id>` | Own devices only. |
| `notifications settings push on\|off` | Joins `settings mail` and `settings watch`. |

A device token is an address, not a credential, but it is
device-identifying and long enough to be awkward in argv. Taking it on
stdin costs nothing and keeps it out of `/proc`; `ReadsStdin: true` is
mandatory or `control.go` swaps in an empty reader and the command
stores an empty string without erroring.

`notifications settings show` and `emitNotificationSettings` grow a
third key, `push`, beside `mail` and `watch`. The map is the JSON
contract, so this is additive.

`device add` on a token that already exists updates the label and the
owner rather than erroring: a reinstall hands the same token to a
different account, and Apple reuses tokens.

Every command runs on every surface, per #234. The app registers over
the JSON API with its bearer token, which is the whole point.

## Delivery

`notify()` in `internal/control/notifications.go` gains a third branch
in the loop it already runs per recipient:

```go
c.Store.AddNotice(id, n.repo.ID, n.kind, c.User.Username, n.action, n.path)
// mail, as today
c.Store.EnqueuePush(id, pushTitle(n), pushBody(n), n.path)
```

`EnqueuePush` writes one row per registered device, and writes nothing
when the account has `notify_push` off or no devices — the same shape as
`ActivityMailAddress` returning "" when `notify_mail` is off. Mute,
watch and actor-exclusion are already settled by `NotifyRecipients`
before this point, so push inherits them for free and cannot drift from
what the inbox shows.

`internal/push` is a `Deliverer` in the mould of `internal/notify`'s
`Mailer`: a two-second ticker, `DuePush(20)`, send, `MarkPushSent` or
`MarkPushFailed` with `RetryBase << (attempt-1)` and dead-lettering at
`DefaultMaxAttempts`. gitbayd starts it beside the mailer at
`cmd/gitbayd/main.go:177`, under the same context, when
`cfg.Push.Enabled`.

The payload:

```json
{
  "aps": {
    "alert": {"title": "krz/gitbay", "body": "cmc opened issue #12"},
    "sound": "default",
    "thread-id": "krz/gitbay"
  },
  "path": "krz/gitbay/issues/12",
  "instance": "https://gitbay.org",
  "user": "cmc"
}
```

`title` is the repository path, `body` the inbox summary — the same
string the inbox row carries, so the two surfaces cannot disagree.
`thread-id` groups a repository's notices in Notification Center.
`path` is the inbox row's `path` field, which the app already knows how
to turn into a link.

`instance` is this instance's `site_url` and `user` the recipient's
username. A device token is one install, and an install registers
against every account signed in on it, so `path` alone cannot say which
account a notice belongs to — two instances can hold the same
`owner/name`. The pair is the account's identity, and the client
resolves it before routing.

`apns-push-type: alert`, `apns-topic` from config, and
`apns-collapse-id` unset — collapsing is wrong here, two comments are
two notices.

Response handling: `200` marks sent; `410`, and `400` with reason
`BadDeviceToken`, delete the device row and its queued rows; `429` and
`5xx` retry with backoff, honouring `Retry-After` when present; other
`4xx` dead-letter with the reason recorded, since retrying a rejected
payload will not fix it.

`internal/notify`'s `redactAddresses` has no analogue to write — a
device token is not a mail address — but the token is never logged
either. Log lines name the device id.

## Assignment notices

`runIssueAssign` (`internal/control/issue.go:423`) files a notice for
each account newly added. The add loop already resolves each name to a
`store.User`; it collects their ids into `added`, and after both loops
succeed:

```go
notify(c, added, notice{repo: repo, kind: "issue", direct: true,
    subject: fmt.Sprintf("[%s] #%d: %s", repo.Path(), issue.Number, issue.Title),
    action:  fmt.Sprintf("assigned you to #%d", issue.Number),
    path:    fmt.Sprintf("%s/issues/%d", repo.Path(), issue.Number)})
```

`direct: true`, as mentions are: an assignment is addressed to someone,
and widening it to watchers would report "assigned you" to people it did
not assign. Removals file nothing. `notify()` already drops the actor,
so assigning yourself is silent.

There is no `mr assign` command; `issue assign` is the only site.

## Web

`/settings/notifications` grows a push row beside mail and watch, and a
device list with a remove button per row, dispatching the same commands
through `runControlStdin`.

There is no web form to add a device — a browser cannot produce an APNs
token. `notifications device add` is therefore reachable on the web in
the sense that every command is, but no page offers it, which is the
Parity page's "CLI only, for now" made literal rather than a refusal.

A new template means a row in `TestMainWidthClass`
(`internal/web/web_test.go`) or CI fails on it.

## The app

Separate merge request on `krz/gitbay-ios`, after the server ships.
`gitbay-ios` has no push scaffolding today: no app delegate adaptor, no
`UNUserNotificationCenter` use, no background modes.

- `UIApplicationDelegateAdaptor` for
  `didRegisterForRemoteNotificationsWithDeviceToken`, which is the only
  way to get the token.
- Permission requested on first visit to the notifications screen, not
  at launch. A prompt before the user has seen what the app does is the
  prompt they deny.
- On the token arriving, and on each sign-in, `notifications device
  add` with a label from `UIDevice.current.name`. On sign-out,
  `notifications device remove`.
- `userNotificationCenter(_:didReceive:)` reads `path` and routes
  through the navigation the inbox rows already use.
- Badge from the unread count the dashboard already returns.

Because an account is an instance plus a user, a device registers once
per signed-in account and holds one row per account it is signed in to.
A token registered against two instances gets two pushes, which is
correct — they are two accounts.

Ships with the Push Notifications capability on `org.gitbay.gitbay`
(team ZCNAX3VL9D), a privacy nutrition label declaring the device token
under Identifiers, and a resubmission.

`DESIGN.org`'s "No push notifications" gap row is rewritten to point at
the implemented feature.

## Sequencing

Server first, app second, in separate merge requests on separate
repositories. The server half is self-contained and testable against a
fake APNs endpoint, which keeps an App Store review off the critical
path. Between the two, `[push]` is configured and inert — nothing has
registered a device, so nothing queues.

`[push]` stays `enabled = false` on bay1 until the app is submitted.

The implementation plan that follows this spec covers the server half
only. The app half is scoped here to fix the contract it has to meet —
the payload keys, the registration calls, the capability and the
nutrition label — and gets its own plan on `krz/gitbay-ios` once the
server has shipped.

## Testing

Unit, `internal/push`:

- The JWT signs, carries `alg: ES256` and the key id in its header,
  `iss` (team id) and `iat` in its claims, and verifies against the
  public half of a generated test key.
- The cached token is reused inside fifty minutes and reminted after.
- Response mapping: 200 sent, 410 and BadDeviceToken reap, 429 and 503
  retry, 403 dead-letters.

Unit, `internal/store`: `EnqueuePush` writes one row per device, none
when `notify_push` is off, none when the account has no devices.

Unit, `internal/config`: each malformed `[push]` is refused at load.

`TestStdinCommandsReadStdin` covers `device add` once it is registered
with `ReadsStdin`; `TestReadOnlyCommandsWriteNothing` covers
`device list`. Both are existing registry tests that pick up the new
commands without being edited — the `cmd/gitbay/main.go` `pass()` table
does need the new commands or its coverage test fails.

E2E, `e2e/push_test.go`: an httptest server standing in for APNs, its
host injected through `GITBAY_APNS_HOST`, following the
`GITBAY_SWEEP_TICK` precedent. An env var rather than a config key, so
`environment` stays a two-name mode that an operator cannot point at a
host that is not Apple's. Register a device, act as another user on
a watched repository, assert the queue drains and the fake received a
payload whose body matches the inbox row's summary. Then a 410 and
assert the device row is gone. The fake speaks HTTP/1.1 — the real
transport is h2 by ALPN, which is stdlib behaviour and not this
repository's to test.

E2E, `e2e/assign_test.go` or the existing issue test: assigning files an
inbox row for the assignee and none for the actor.

Verified already: outbound HTTP/2 from bay1 to `api.push.apple.com:443`
reaches Apple — a GET to `/3/device/test` answers `405` over h2.

## Docs

- Wiki `Parity`: rows for the four commands, in the merge request that
  adds them.
- Wiki `Admin`: the `[push]` section, obtaining a `.p8`, and the
  one-instance-one-bundle-ID constraint for self-hosters.
- Wiki `Users`: `notifications settings push`, and what a device row is.
- `CHANGELOG.org`.
- `krz/gitbay-ios` `DESIGN.org`: the gap row.
