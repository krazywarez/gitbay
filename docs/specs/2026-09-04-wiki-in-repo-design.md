# Wikis in the repository

Ref #169, #170. Milestone v1.14.0. Two merge requests: path filters first,
because without them the second one makes the wiki worse to use than it is
today.

## Problem

A wiki is a companion bare repo at `<owner>/<name>.wiki.git`, created on first
push (`internal/sshd/sshd.go:359-362, 473-480`). It has no row in the store;
access derives from the parent.

That buys one thing — prose edits stay out of the code repository's history,
its protected branches and its builds — and costs four:

- **Invisible to the store.** Backup verification cannot distinguish a wiki
  from a leaked directory and prints so (`cmd/gitbayd/backup.go:270`). Quotas,
  `repo list`, search and the activity feed do not see it either.
- **Push is the only write path.** There is no `wiki edit` command and the web
  renders wikis read-only. It is the one capability that does not follow "the
  capability lands as a control command, then the surfaces render it".
- **`.wiki` is a reserved name suffix**, permanently
  (`internal/policy/names.go:62`).
- **A second clone URL** that cannot be discovered without knowing the
  convention.

## Approach

Pages move to `.gitbay/wiki/` on the default branch, beside `ci.yml` and
`CODEOWNERS`, which is already where repository-scoped gitbay metadata lives.
The companion path is removed rather than kept alongside: one wiki exists across
70 repositories on this instance, so a compatibility path would be permanent
cost for a single migration.

Rejected: an orphan ref (`refs/wiki/main`) in the main repository. It keeps
prose off the code DAG and out of normal clones, but it is invisible to plain
git tooling, needs a custom refspec to fetch, and would need its own write
commands to be usable at all. The gain over a directory is that prose stays out
of `git log`; the cost is a wiki nobody can edit without forge-specific
instructions.

## What this does and does not buy

**Does:** one clone, one backup, one permission model, one history. Wiki edits
become reviewable through merge requests, approvals and CODEOWNERS for projects
that want that.

**Does not:** web editing on every repository. `repo commit-file` is the command
behind the web editor, and it refuses repositories that require verified
signatures, because the server authors those commits unsigned and will not write
a commit the repository's own policy would reject
(`internal/control/commitfile.go:28-35`). `krz/gitbay` requires signed commits,
so its wiki stays push-only. That is not a regression — it is push-only today —
but the parity gain is conditional and should not be claimed otherwise.

## Design

### Phase 1 — path filters (#169)

`ci.Job` gains `Paths` and `PathsIgnore`, each a list of globs matched with
`path.Match` against the changed-file list from `gitutil.DiffFiles(dir, old,
new)`, which already exists (`internal/gitutil/merge.go:276`).

A job runs when its `Paths` is empty or at least one changed file matches it,
and no `PathsIgnore` pattern matches every changed file.

**Fail open.** A job runs whenever the filter cannot be evaluated: a new branch
with no diff base (`old` is empty or all zeros), a `DiffFiles` error, or a job
declaring neither key. A filter that silently skips CI when it cannot tell is
worse than no filter, because the failure is invisible.

`QueueBranchBuilds` is shared with the merge path, which moves a ref without
reaching a hook (`internal/hookd/hookd.go:272-278`), so the old sha must reach
both callers. `u.Old` is already available at the hook call site
(`hookd.go:212`).

Tag jobs are unaffected: a tag build has no meaningful diff base.

### Phase 2 — the move (#170)

**Storage.** `.gitbay/wiki/*.{md,org,markdown}` on `repo.DefaultBranch`.
`wikiExts` is unchanged.

**Resolution.** `wikiPages` and `wikiHome` keep their logic; they read a tree at
`.gitbay/wiki` on the default branch instead of the root of the companion's
`main`. `wiki list` and `wiki show` keep their argv, their JSON fields and their
exit codes — only resolution moves, so no surface changes shape.

`HasWiki` (`internal/httpd/web.go:336`) becomes "the default branch holds a
non-empty `.gitbay/wiki/` tree". The web route `/{owner}/{repo}/wiki` is
externally identical.

**Writing.** A push, like any other file. `repo commit-file <owner/name>
.gitbay/wiki/Page.md --ref <branch> --file -` is the existing command and the
existing web editor path; no `wiki edit` is added, because it would duplicate
one.

**Removal.** The `.wiki` suffix branch and `runWikiGit` in
`internal/sshd/sshd.go`; `wikiDir` in `internal/control/wiki.go` and
`internal/httpd/wiki.go`; the reservation in `internal/policy/names.go:62` and
the test asserting it; companion rename and delete in
`internal/control/repo.go:396,452`; the special-case wording in
`cmd/gitbayd/backup.go:270`.

**Migration.** One repository, by hand, not a shipped command:

```
git bundle create gitbay-wiki-$(date +%F).bundle --all   # in a clone of the companion
git subtree add --prefix=.gitbay/wiki <wiki-url> main
```

`git subtree add` preserves the wiki's history inside the repository's DAG
rather than flattening it into one import commit. Verify pages render, keep the
bundle, then remove the bare repo from the server.

Add `paths-ignore: [".gitbay/wiki/**"]` to this repository's own heavy jobs in
the same change, so the migration does not immediately demonstrate the problem
phase 1 exists to prevent.

## Tests

Phase 1:
- A job with `paths` matching a changed file runs; one matching nothing does not.
- `paths-ignore` covering every changed file skips the job; covering some of
  them does not.
- A new branch runs every job.
- A `DiffFiles` failure runs every job.
- A job with neither key runs, unchanged from today.
- Tag builds are unaffected.

Phase 2:
- `wiki list` and `wiki show` return the same JSON for a repository whose pages
  are in `.gitbay/wiki/` as the old commands returned for a companion.
- The web wiki tab renders, and reports no wiki when the directory is absent.
- A repository with no `.gitbay/wiki/` reports no wiki rather than erroring.
- Pushing to `<name>.wiki.git` is refused, since the route is gone.
- A repository may now be named `something.wiki`.
- `repo commit-file` writes a page on a repository that permits it, and is
  refused on one requiring verified signatures.

## Documentation

CLAUDE.md's "the repo's own documentation lives in the wiki" stops being true of
the storage and needs rewording. The wiki's Parity rows for wiki capabilities
change, and the "SSH only, by design" list does not mention wikis, so it needs
no edit.
