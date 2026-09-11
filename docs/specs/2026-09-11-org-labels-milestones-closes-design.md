# Org-level labels and milestones, cross-repository closes

Closes #203 (ref #185). Labels and milestones an org defines once for every
repository under it, and `Closes owner/name#N` acting on another repository
the actor can write to.

## Problem

Labels and milestones are rows keyed on `repo_id`; `closes #N` acts in the
repository the commit landed in (`internal/control/commitrefs.go`). An org
with several repositories recreates its labels in each, keeps a milestone
per repository for one release, and cannot close `ttorg/widget#1` from a
commit to `ttorg/lib`. The web already links `owner/name#N` across
repositories (`internal/autolink`, with a read check); only the action is
missing.

## Decision

All three move beyond the repository:

- An org holds labels and milestones. Every repository owned by the org
  sees them beside its own. Org rows are managed by org admins through
  `org label` and `org milestone`.
- `Closes owner/name#N` in a commit on the default branch, or in a merged
  merge request's title or body, closes that issue when the pusher or
  merger holds write on the target. Otherwise the text stays a plain
  autolink.

Decisions taken on the way, with the alternatives rejected:

- **Scope columns on the existing tables**, not separate `org_labels` and
  `org_milestones` tables. `issue_labels` and the two `milestone_id` columns
  keep pointing at the same ids, so attaching, filtering and counting do
  not fork into two sources. The cost is a table rebuild in the migration.
- **Inherited, not templated.** An org label is one row every repository
  reads, not a copy made at repository creation. Copies drift, which is
  what #203 complains about.
- **Any writable target for closes**, not same-org only. Write on the
  target is the permission `issue close` needs there; the org boundary
  would be narrower than the model and one more rule to explain.
- **Org admins manage org rows.** Repository write is enough for repo rows
  today; the org's rows affect every repository, so the org's admin role
  is the gate.
- **Promote on org create.** `org label set bug` when repositories under
  the org already hold `bug` folds them into the org row rather than
  refusing. Refusing would make the migrant's first command fail against
  exactly the duplication they came to remove.
- **Org pages under `/{org}/-/`.** A hyphen cannot start a repository
  name, so `/{org}/-/labels` shadows nothing and reserves nothing.
- **Org writes are CLI and API only.** The repository label page's form
  exists for colour alone; three org forms nobody asked for are not
  worth their handlers. Recorded in Parity as deliberate.

## Data

Migration 0052 rebuilds `labels` and `milestones` the way 0041 rebuilt
`commit_statuses`: rename, create, copy with ids, drop. Unlike
`commit_statuses`, both tables have children (`issue_labels`,
`issues.milestone_id`, `merge_requests.milestone_id`), and since SQLite
3.26 `ALTER TABLE RENAME` rewrites a child's foreign key to follow the
renamed parent, which would leave the children pointing at `labels_old`.
The script therefore brackets the renames with `PRAGMA legacy_alter_table
= ON` and `= OFF`, which a transaction allows; the children keep naming
`labels` and `milestones` and bind to the new tables. `foreign_keys` stays
on, so the copy is checked and the drop of the old tables cascades
nothing, since nothing references them. The migration test runs `PRAGMA
foreign_key_check` afterwards and expects no rows.

```sql
CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    org_id  INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    CHECK ((repo_id IS NULL) <> (org_id IS NULL))
);
CREATE UNIQUE INDEX labels_repo_name ON labels(repo_id, name) WHERE repo_id IS NOT NULL;
CREATE UNIQUE INDEX labels_org_name  ON labels(org_id, name)  WHERE org_id  IS NOT NULL;
```

`milestones` keeps `title`, `description`, `due_date`, `state`, `created_at`
and gets the same `repo_id`/`org_id` pair, CHECK and two partial unique
indexes in place of `UNIQUE (repo_id, title)`.

The down migration recreates the old shape and fails if any org-scoped row
exists; there is no repository to give such a row to.

`store.Label` and `store.Milestone` gain `OrgID int64` beside `RepoID`.

## Resolution

Store lookups that today take a `repoID` take the `store.Repo` and derive
the scope: `repo_id = ?` for a user-owned repository, `repo_id = ? OR
org_id = ?` with `repo.OwnerID` when `repo.OwnerKind == "org"`.

- **Listing** for a repository returns org rows then repo rows, each by
  name. `label list`, `milestone list` and the web pages mark org rows.
- **Attaching** by name (`issue label --add`, `issue milestone`, `mr
  milestone`) resolves the org row when one exists, else the repo row.
  `issue label --add` still creates a repo label on the fly when neither
  exists.
- **Repo-level create** (`label set`, `milestone create`) is refused when
  the org holds the name: `bug is an org label; set it with org label set
  <org> bug`. Exit 1. `label remove`, `milestone close` and `milestone
  reopen` refuse an org row the same way.
- **Org-level create** when repositories under the org hold the name
  promotes, in one transaction: insert the org row, repoint
  `issue_labels.label_id` (or `issues.milestone_id` and
  `merge_requests.milestone_id`) from each repo row to it, delete the repo
  rows. The colour, description and due date are the ones on the command.
  The reply names how many repositories were folded in.
- **Filtering** (`--label`, `--milestone`, the web filters) resolves the
  name the same way, so an org milestone filters a repository's list like
  a repo one.
- **Counting.** An org label's use count and an org milestone's open and
  closed totals span the org's repositories the caller can read. The store
  takes the readable repository ids the caller already gets for `repo
  list` and restricts the count subqueries to `repo_id IN (...)`. An
  anonymous web viewer counts public repositories only.

## Commands

One file, `internal/control/orglabel.go`.

```
org label set        <org> <label> [--color rrggbb|'']
org label list       <org>                              ReadOnly
org label remove     <org> <label>
org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]
org milestone list   <org> [--state open|closed|all]    ReadOnly
org milestone close  <org> <title>
org milestone reopen <org> <title>
```

Writes require org admin, via `OrgRole`, the gate `org members add` uses.
Reads require membership or a public repository under the org; an outsider
gets "denied", not "no organization", as `org show` answers today. `org
label set` on an existing org label sets the colour. `org label remove`
takes the label off every issue in the org through the existing cascade.

JSON: `org label list` returns `[{name, color, uses}]`; `org milestone
list` returns the milestone rows with `open` and `closed` counts, as
`milestone list` does. Each command gets a `pass()` in `cmd/gitbay/main.go`.

## Closes

`closePat` gains an optional path prefix:

```
(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[ :]+(?:([a-z0-9][a-z0-9._-]*)/([a-z0-9][a-z0-9._-]*))?#(\d+)\b
```

`ProcessCommitMessages` and `ProcessMRDescription` resolve a prefixed match
with `RepoByPath`, look up the actor's grant on the target with
`AccessRole`, and act only if `policy.CanWrite`. An unknown path or a
refused target does nothing and logs nothing above debug; the text remains
an autolink only readers see. On success `actOnIssue` runs against the
target: the issue closes, the system comment links the source commit by
full path, the `issue.closed` event lands on the target's feed, and the
once-per-(issue, sha) record applies. Bare `owner/name#N` without a
keyword stays display-only.

## Web

- `GET /{owner}/-/labels` and `GET /{owner}/-/milestones` for an org,
  rendered from `labels.html` and `milestones.html` with the org as scope
  and no edit form, linked from the org page. 404 for a user owner, and
  for an org the viewer cannot see any repository of.
- Repository label and milestone pages show org rows with an "org" mark
  and no edit control.
- Issue and merge request lists, filters and the milestone picker need
  only the store change; templates gain the mark.
- No new event kinds; label and milestone changes are configuration.

## Docs

- Users: an "Org labels and milestones" paragraph after the milestones
  one, and `Closes owner/name#N` in the commit-references paragraph.
- Parity: rows for `org label set/list/remove`, `org milestone
  create/list/close/reopen` (CLI yes, web read-only, API yes) and
  cross-repo closes; org writes recorded as deliberately CLI-only.
- FAQ: nothing, the question no longer needs a "not planned" answer.
- CHANGELOG entry under the next version.

## Tests

- Store: migration 0052 over seeded repo labels and milestones attached
  to issues and MRs, ids and memberships intact and `PRAGMA
  foreign_key_check` empty; promote folding two
  repositories' `bug` into one org row; repo-level create refused against
  an org name; counts restricted to a readable set.
- Control: per command, org admin versus member on writes, outsider
  wording on reads; `issue label --add` resolving to the org row;
  `--milestone` filtering by an org milestone in `issue list` and `mr
  list`.
- Closes, in `commitrefs_test.go`: prefixed close with write on the
  target closes; without write leaves it open; unknown path ignored; plain
  `#N` unchanged; once per issue and sha; the MR description path.
- e2e, `e2e/orglabels_test.go`: an org with two repositories, `org label
  set` then an issue in each carrying it, an org milestone with progress
  across both, a push to one repository closing an issue in the other, the
  two org pages for a member, and a private org's pages for an outsider.
- `TestReadOnlyCommandsWriteNothing` and the CLI coverage test cover the
  new commands without additions.

## Rollout

One MR. Migration 0052 runs on daemon start; the rebuild copies every row
once and is fast at this scale. No config, no runner change, no client
change. Ships in the next minor version, since it adds commands and a
migration.
