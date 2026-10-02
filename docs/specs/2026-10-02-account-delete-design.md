# Account deletion

Closes #322. An account deletes itself. Today only `admin user delete`
exists, and it refuses an account that anchors issues, merge requests,
comments, reviews or an org with no other admin.

## Decision

`account delete` is a write on every surface. It takes the typed
username, mails a confirmation link to the primary verified address,
and on confirmation marks the account for deletion seven days out.
During those seven days the account is refused everywhere except the
two ways a person signs in, either of which cancels. After seven days
a reaper purges it: owned content goes, authored content on other
owners' repositories moves to a `ghost` account.

## States

| state | set by | SSH (full scope) | SSH (other scopes), API tokens | web session | web login link |
|---|---|---|---|---|---|
| requested | `account delete --confirm <name>` | normal | normal | normal | normal |
| scheduled | the mailed link | cancels, then normal | refused | ended | cancels, then normal |
| purged | the reaper | no account | no account | no account | no account |

- *requested* is a row in `account_deletions` (user_id, token_hash,
  requested_at, expires_at; the link lives 24 hours). Nothing about the
  account changes. A second request replaces the first.
- *scheduled* sets `users.delete_after` (now + 7 days) and ends every
  web session. A full-scope SSH session or a completed login link clears
  `delete_after`, is audited `account.delete.cancelled`, and prints or
  shows "deletion of your account was cancelled". Runner-, read- and
  deploy-scoped keys and API tokens are refused with "this account is
  scheduled for deletion on <date>; sign in to cancel" so automation
  cannot cancel by accident.
- *purged*: the reaper, on the tick that already runs
  `ReapPendingUsers`, takes every account with `delete_after` in the
  past.

## Purge

One function in `internal/control` (it needs the repository root), run
from the reaper, in this order:

1. Re-check the org rule. If the account is now the only admin of an
   org, skip, leave `delete_after` set, audit
   `account.delete.blocked`, and show it in `admin user show`. An
   instance admin resolves it.
2. Delete every repository the account owns through `deleteRepo`, the
   same path as `repo delete` (open MRs sourced from them are marked
   source-gone; the directories go).
3. In one transaction: reassign `issues.author_id`,
   `merge_requests.author_id`, `issue_comments.author_id`,
   `mr_comments.author_id`, `mr_diff_comments.author_id` and
   `mr_reviews.reviewer_id` to the ghost; then `DeleteUser`, whose
   anchor check now passes. Everything else is already `CASCADE` (keys,
   emails, tokens, sessions, snippets, memberships, watches, reactions,
   assignments, inbox, review requests) or `SET NULL` (audit actor,
   event actor, signatures, statuses, merged/closed by, releases).
   Grants and the profile backfill row go in the same transaction, as
   `DeleteUser` already does.
4. Audit `account.delete.purged` with the username.

The username is free again after the purge. User ids are never reused
(#306).

## The ghost

A real `users` row named `ghost`, created by the first purge that needs
it and flagged `users.ghost = 1`: it has no keys, emails or sessions,
cannot be signed in to, own anything, be granted access, or be deleted.
`ghost` is added to `internal/policy/names.go` so nobody can register
it. If an instance already has a real account named `ghost`, the purge
refuses with a message telling the operator to rename it; gitbay.org
has none. Its profile page reads "This account stands in for deleted
users." Content shows `ghost` as its author, the way GitHub shows it.

## Surfaces

- `account delete --confirm <username>`: refuses a mismatched name
  (exit 2), an account with no verified address (exit 4, "add and
  verify an address first"), and the only admin of an org (exit 4,
  naming the orgs). Prints where the mail went and suggests
  `account export`.
- `account delete --cancel`: clears a request or a schedule (the
  scheduled case is reachable only from a full-scope key, which already
  cancels on connect; this exists for the requested state).
- Web: a "Delete account" section at the bottom of `/settings` with
  the `confirmfield` partial, posting the same command. The mailed link
  opens `/settings/delete?token=<token>`, a page that names the purge date
  and has one button; the GET changes nothing.
- The mail names the purge date, says how to cancel, and suggests
  `account export`.
- `admin user show` reports `delete_after` and a blocked purge.

## Migration

`account_deletions`; `users.delete_after TEXT`; `users.ghost INTEGER
NOT NULL DEFAULT 0`.

## Docs

Parity row; Users wiki page (a "Deleting your account" section);
`/privacy` text; Terms wiki page's "Leaving" section, which today says
to mail the operator.

## Not doing

- Deleting authored content on other owners' repositories. Decided on
  #322: it moves to the ghost.
- An admin-initiated version with the grace period. `admin user delete`
  stays as it is.
