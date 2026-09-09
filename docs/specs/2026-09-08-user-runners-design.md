# Runners attached to repositories

Ref #184 (option B). A `gitbay-runner` anyone installs on their own machine
and attaches to their repositories on any instance, so an instance offers CI
without offering compute.

## Problem

CI on gitbay.org builds the forge's own repositories and nothing else: the one
runner shares the host with the forge and is scoped with `-repos`. A
`.gitbay/ci.yml` in anyone else's repository queues builds nothing claims.
Widening that runner's scope is the second machine and the tier-per-trust-level
design in #184, which costs compute and storage the operator pays for.

The runner already polls over SSH from anywhere with a key of scope `runner`,
and `admin runners` already lists several. What is missing is the server-side
rule that says which builds a given key may claim. Today there is none:

- `keys add --scope runner` is self-service for any account.
- `runner next` checks only the key's scope (`requireRunner`). With no
  repository arguments it claims the oldest pending build on the instance,
  whichever repository it belongs to, and the claim carries the repository's
  secrets when the build is trusted.
- `runner log` and `runner done` accept any build id.

With `registration = "open"` a stranger can run a runner against gitbay.org
today and receive builds and secrets for repositories they cannot read. The
wiki says a runner account is admin by necessity; the code does not enforce
it.

## Decision

A runner key is attached to repositories by a repository admin, and claims
builds only for the repositories it is attached to. A user who wants builds
installs `gitbay-runner`, runs `gitbay-runner init`, attaches the printed
public key to their repository, and starts the service. Admin keys keep
today's behaviour. Untrusted builds (merge request heads from forks) are
excluded from every claim unless the runner asks for them.

Decisions taken on the way, with the alternatives rejected:

- **Repository-level attachment**, not "a user's runner builds the user's
  repositories" and not "repositories the user can admin". One attachment
  row answers "who executes this repository's code" exactly.
- **The runner prints its key and an admin attaches it**, not a
  registration token. No secret crosses the wire, and it is the deploy-key
  motion the forge already has.
- **An attachment table keyed by ssh key**, not a `runner:<repo>` scope on
  the key. Fingerprints are unique per instance, so a scope binds one key
  to one repository; a table lets one key serve many.
- **Untrusted builds skipped by default**, enforced by the server, with a
  runner flag to opt in. Not "build everything" and not "the runner refuses
  without podman", which would be the runner's word.
- **Homebrew formula in `krz/homebrew-tap` on gitbay.org**, built from
  source at the tag like `gitbay.rb`. Not a GitHub tap, not an installer
  script.

Not in scope: per-account build limits, claim order, a second operator
machine. Those stay on #184.

## Server

### Data

Migration 0050:

```sql
CREATE TABLE runner_repos (
    key_id   INTEGER NOT NULL REFERENCES ssh_keys(id) ON DELETE CASCADE,
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (key_id, repo_id)
);
```

`runner_seen` is rekeyed from `user_id` to `key_id` (SQLite: recreate the
table; existing rows are dropped, they are heartbeats). `user_id` stays as a
plain column for the listing. Two runners on one account are two rows.

`control.Ctx` gains `KeyID int64`, the id of the key that authenticated the
session, set by `internal/sshd`. Web and API sessions leave it zero; every
command that reads it is `SSHOnly`.

### Commands

One new noun under `repo`. Each requires `policy.CanAdmin` on the repository.

| Command | Flags | Notes |
|---|---|---|
| `repo runner add <owner/name> < key.pub` | `ReadsStdin` | Unknown fingerprint: added to the caller's account with scope `runner`, then attached. Known fingerprint: must already have scope `runner`, and must belong to the caller unless the caller is an instance admin, else exit 4. A full-scope or deploy key is never promoted. Attaching an already-attached key is exit 0 and idempotent. The key's account must be able to read the repository, or the runner cannot clone it. Output `{fingerprint, repo}`. |
| `repo runner list <owner/name>` | `ReadOnly` | Per key: fingerprint, algo, owner username, `added_at`, `last_seen`, and the build it holds (`build_number`, `build_job`, `started_at`) when it holds one. |
| `repo runner remove <owner/name> <fingerprint>` | | Drops the attachment. The key stays on the account; `keys remove` drops the key and cascades. Exit 3 when not attached. |

### Claim rule

`runner next [--untrusted] [<owner/name>...]`:

- Scope `runner`: the candidate set is the key's attachments. No attachments
  claims nothing and returns "no pending builds". Each `<owner/name>` on the
  command line must be among the attachments, else exit 4 naming it; the
  named set narrows the candidates.
- Admin key: unchanged. Any repository, narrowed by the arguments.
- Without `--untrusted`, builds with `trusted = 0` are never claimed, for
  every key. `store.ClaimBuild` gains the parameter. The bay1 unit passes
  `-untrusted` to keep building fork heads in podman.
- Secrets ride the claim as today (`b.Trusted`). An attached runner was
  attached by a repository admin and is trusted with the repository's
  secrets.
- The orphan skip loop, the reachability check and the heartbeat are
  unchanged. The heartbeat records `c.KeyID`.

`runner log <id>` and `runner done <id> ...` on a scope-`runner` key: the
build's repository must be attached to the key, else exit 4. Admin keys are
unchanged.

### Listings

`admin runners` rows carry `fingerprint` beside `username`. `scope` becomes
the attached repositories for a runner key, joined with commas; for an admin
key it stays the repositories the runner asked for, or `any`. `dashboard` reads the same rows.

## Runner

### `gitbay-runner init`

A subcommand, `gitbay-runner init [-remote git@host] [-isolation podman|none]`.
It:

1. Creates the config directory: `$XDG_CONFIG_HOME/gitbay-runner`, else
   `~/.config/gitbay-runner`. Mode 0700.
2. Generates `id_ed25519` and `id_ed25519.pub` there unless present. Mode
   0600 on the private key. The runner never overwrites a key.
3. Writes `config.toml` unless present:

   ```toml
   remote = "git@gitbay.org"
   workdir = "/Users/x/Library/Caches/gitbay-runner"   # defaultWorkdir()
   isolation = "podman"
   untrusted = false
   ```

   `isolation` is `none` unless `-isolation podman -image <ref>` are both
   given: the runner refuses podman without an image, and there is no
   image to guess. With `none` it prints one line saying so: steps run as
   this user, and untrusted builds are excluded by default so that means
   your own commits.
4. Prints the public key and the next step:

   ```
   gitbay repo runner add owner/name < /Users/x/.config/gitbay-runner/id_ed25519.pub
   ```

   and the URL to paste it at, `https://<host>/<owner>/<name>/settings`,
   with `<host>` taken from the remote. Then `brew services start
   krz/tap/gitbay-runner`, or the binary with no arguments.

Re-running `init` is safe and prints the same key.

### Config file

The daemon reads `config.toml` from the config directory when it exists.
Keys are the flag names (`remote`, `ssh-opts`, `clone-base`, `workdir`,
`poll`, `timeout`, `repos`, `jobs`, `image`, `isolation`, `memory`, `cpus`,
`untrusted`, `identity`). A flag given on the command line overrides the
file. `-config <path>` names another file. No other configuration source.

### Own identity

`-identity <path>`, default the generated key when it exists, else empty.
When set, ssh and git clone get `-i <path>` and `-o IdentitiesOnly=yes`, so
a laptop's ambient full-scope key is never offered. Under podman the clone
already happens outside the container; the identity stays outside with it.

### `-untrusted`

Adds `--untrusted` to `runner next`. Default off.

### Packaging

- `Formula/gitbay-runner.rb` in `krz/homebrew-tap`: source build at the
  tag, `go build ./cmd/gitbay-runner`, a `service do` block running
  `opt_bin/"gitbay-runner"` with no arguments, `keep_alive true`, logs under
  `var/"log"`, and caveats naming the two steps. `test do` asserts
  `-version`.
- `gitbay.rb` in the same tap moves from v0.4.0 to the current tag in the
  same commit.
- `deploy/release.sh` builds `gitbay-runner` for the three targets alongside
  `gitbay` and `gitbayd`.

Unchanged: poll interval, workdir layout, the per-repository build home,
SIGTERM drain, podman isolation, log cap.

## Web

The repository settings page gains a Runners section: the `repo runner list`
rows with a remove button each, and a textarea that posts a public key. Both
go through `settingsSubmit` into the control commands; the paste is stdin
via `dispatchIntoStdin`. No new route, so no reserved name. The account page
already lists keys with their scope; a runner key shows as `runner`.

## Docs

- `Users`: a "Your own runner" section under CI builds: install, `init`,
  attach (CLI and web), start, what it builds (your commits, with secrets)
  and what it does not (fork heads, unless `-untrusted`), several
  repositories on one runner, several runners on one account.
- `Admin` and `Threat-Model`: replace "a runner account is admin by
  necessity" and "the scoping is what the runner asks for, not an ACL the
  server holds" with the attachment rule. The bay1 example gains
  `-untrusted`.
- `Parity`: three rows, `repo runner add|list|remove`, yes on SSH, CLI, web,
  API.
- `CI` and `FAQ`: one line each pointing at the Users section. The FAQ's
  "CI builds only the repositories the operator names" becomes "and any
  repository with a runner attached".

## Tests

- Store: `ClaimBuild` skips untrusted builds unless asked; a claim limited to
  attached repositories; attachment rows go with the key and with the
  repository; `runner_seen` per key.
- Control: a scope-`runner` key with no attachment claims nothing; an
  attached key claims its repository and not another user's pending build;
  a named repository outside the attachments is exit 4; `runner log` and
  `runner done` refused for an unattached build; `repo runner add` refuses
  a full-scope key and a deploy key; add is idempotent. The existing
  `TestStdinCommandsReadStdin`, `TestReadOnlyCommandsWriteNothing`, the CLI
  table coverage test and the Parity test cover the new commands without
  changes. The e2e tests that add a scope-`runner` key today
  (`buildcancelweb_test.go`, `reap_test.go`, `runner_scope_test.go`) gain an
  attach step, since an unattached key no longer claims.
- Runner: `init` writes key and config with the right modes and never
  overwrites; config values are overridden by flags; `-identity` reaches the
  ssh and git command lines.
- e2e, one test: `init` against the test instance, attach over SSH with the
  printed key, start the runner with the generated config, a push builds
  and succeeds, a merge request head from a fork stays pending; with
  `-untrusted` it builds.

## Rollout

1. Server, store, control, web, docs, tests: one MR against `main`, with
   migration 0050. Deployed, the change closes the claim hole for every
   existing scope-`runner` key on the instance: none has attachments, so
   none claims. That includes the bay1 runner, which polls as the
   non-admin account `ci` with a scope-`runner` key. Right after the
   deploy, attach it as the admin:

   ```
   ssh -p 2222 root@bay1 cat /var/lib/gitbay-runner/.ssh/id_ed25519.pub \
     | gitbay repo runner add krz/gitbay
   ssh -p 2222 root@bay1 cat /var/lib/gitbay-runner/.ssh/id_ed25519.pub \
     | gitbay repo runner add cmc/ci-smoke
   ```

   Builds queued between the deploy and the attach wait; none is lost.
   `-untrusted` goes into the unit in the same deploy.
2. Runner: `init`, config file, `-identity`, `-untrusted`. Same MR or the
   next; the server change does not depend on it.
3. Tap: formula and the `gitbay.rb` bump, after the release that carries
   the runner change is tagged.
