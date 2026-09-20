# Profile about text in a repository

Closes #236.

The `about` text on a user or org profile is a column on `users`/`orgs`,
set through `profile set --about` and rendered by `aboutHTML`. It is the
only long-form, version-worthy prose on the instance that is not a file
in a repository. This moves it into one.

Links, description and website stay where they are. The issue lists
links as a "consider"; moving them buys nothing the DB columns do not
already give, and a second file or a front-matter parser is cost without
a return.

## Where it lives

`profile/README.md` or `profile/README.org` on the default branch of a
repository named `.gitbay` under the owner's namespace:

```
cmc/.gitbay
└── profile/
    └── README.org
```

Resolution order is the wiki's `wikiExts`: `.md`, `.org`, `.markdown`;
the first that exists wins. There are no store rows for the file —
access derives from the parent repository, exactly as
`internal/control/wiki.go` states for wiki pages.

A dot-repo rather than the GitHub-style `cmc/cmc` because `.gitbay` is
not single-purpose: it is the place later per-owner configuration
(issue templates, org defaults) goes, and a leading dot is the signal
that it is infrastructure rather than a project.

### Reading

A helper in `internal/control/profile.go`:

```go
// ownerAbout returns the about text and its format for an owner, read
// from profile/README.* on the default branch of <owner>/.gitbay.
// Everything missing — the repo, the branch, the file — is an empty
// about, as is a repo the caller cannot read.
func ownerAbout(c *Ctx, owner string) (text, format string)
```

It resolves `<owner>/.gitbay` through the same access check every other
read takes, so a repository the caller cannot read yields no about. The
blob read is capped at `maxCommitFileBytes` (1MB).

`ProfileOut.About` and `ProfileOut.AboutFormat` keep their JSON names
and meanings; only the source changes. `AboutFormat` is `org` for a
`.org` file and `md` otherwise. The API contract and the iOS client are
untouched.

One field is added: `about_path`, the repository-relative path the text
was read from, empty when there is no about. The web needs it to link to
the file rather than guess its extension, and every other client gets
the same pointer.

`aboutHTML` in `internal/httpd/web.go` becomes a direct
`renderReadme(name, raw)` call — there is a filename to dispatch on now,
so the stored-format indirection goes away.

## Naming

`policy.namePat` requires a leading alphanumeric, so `.gitbay` is an
invalid repository name today:

```go
var namePat = regexp.MustCompile(`^\.?[a-z0-9][a-z0-9._-]{0,61}$`)
```

One optional leading dot, same 63-character ceiling. `ValidateName`
keeps refusing `.`, `..` and a `.git` suffix, and gains an exact-`.git`
refusal — the suffix rule only fires for names longer than four
characters.

Relaxing the pattern rather than whitelisting the one name `.gitbay` is
the smaller change, and it gives owners `.dotfiles` and the like for
free.

## A first commit into an empty repository

`gitutil.CommitFileChange` resolves the target branch and fails when it
does not exist, so committing the first file into a freshly created
`.gitbay` is impossible today. Both the web's create button and the
backfill need it to work.

An unresolvable branch becomes a root commit **only when the repository
has no refs at all**. Anywhere else it stays the error it is now — a
typo'd branch name in a repository with history must not silently start
an orphan branch.

This also makes `repo commit-file` work on a repository created but
never pushed to, which is the same gap seen from the CLI.

## Writing

There is no about-specific write command, for the reason the wiki has
none: the content is a file, and the file is written the way files are
written.

- `profile set` loses `--about`, `--about-format` and `--file`, and
  loses `ReadsStdin`.
- `org profile` loses the same three flags.
- Authoring is a push, or
  `repo commit-file cmc/.gitbay profile/README.org --ref main --file -`.

### The web

The About textarea on the account page is replaced by a line naming the
file and linking to it, with a button that creates `<owner>/.gitbay`
and commits a starter `profile/README.md` when the repository does not
exist yet. Editing then happens in the repository file editor that
already exists.

The alternative — keeping the textarea and dispatching
`repo commit-file` from it — needs an auto-create path and has a stale
file problem: moving the format picker from md to org leaves a
`README.md` that keeps winning resolution, and `commit-file` cannot
delete it, so the handler needs a second dispatch to `file remove`. The
pointer is smaller and matches how a wiki page is edited.

The account form's `profile` case keeps `--description`, `--website`
and `--link`, and stops sending `--about-format` and stdin. The Preview
button on that form goes with the textarea; the repository file editor
has its own.

## Access and visibility

The about renders to whoever can read `<owner>/.gitbay`. A private
`.gitbay` means the about is visible to the owner and admins only. That
is the parent-derived rule already in force for wiki pages, not a new
one.

The backfill creates the repository **public**, so no about that was
world-readable becomes hidden by the move.

Repositories whose name starts with `.` are filtered out of:

- the profile page's repository list (`ProfileOut.Repos`), and
- `explore`.

They stay in `repo list`, which is the owner's own inventory, and stay
reachable at their URL. Hiding the repository is what a dot-repo buys
over `cmc/cmc`; without the filter the move trades one visible
single-purpose repository for another.

## Migration

A SQL migration cannot write git objects, so the move is two pieces
that ship together. `gitbayd` runs `MigrateUp` at startup, so a backfill
that reads the columns must not run after a migration that drops them —
hence the holding table.

**Migration 0058** copies every owner with a non-empty about into a
holding table, then drops the columns:

```sql
CREATE TABLE profile_about_backfill (
  owner_kind   TEXT NOT NULL,
  owner_id     INTEGER NOT NULL,
  about        TEXT NOT NULL,
  about_format TEXT NOT NULL,
  PRIMARY KEY (owner_kind, owner_id)
);
INSERT INTO profile_about_backfill ...  -- users, then orgs
ALTER TABLE users DROP COLUMN about;    -- and about_format
ALTER TABLE orgs  DROP COLUMN about;    -- and about_format
```

The down migration re-adds the columns empty and drops the table.

**`gitbayd admin migrate-profile-about`**, in the shape of
`adminMigrateCommitRefsCmd` in `cmd/gitbayd/adminusers.go`, drains the
table. Per row: create `<owner>/.gitbay` public if it does not exist,
`gitutil.CommitFileChange` the README at the recorded format, delete
the row. Idempotent — an owner who already has the file is skipped and
their row deleted.

Commit identity is the owner's primary verified email when they have
one, otherwise `<name>@users.noreply.<host>`. Orgs have no email and
always take the fallback.

A later release drops the emptied holding table.

## Testing

Unit:

- `policy`: `.gitbay` and `.dotfiles` accepted; `.`, `..`, `.git` and
  `x.git` still refused; the 63-character ceiling holds with the dot.
- `ownerAbout`: `.md` wins over `.org`; a missing repo, a missing
  branch and a missing file each give an empty about; a repo the caller
  cannot read gives an empty about.
- `gitutil.CommitFileChange`: the first commit into an empty repository
  succeeds and reads back; the second takes the parented path; an
  unknown branch in a repository with history is still an error.

e2e, new `e2e/profileabout_test.go`:

- a committed `profile/README.md` shows on `profile show` and on the
  web profile page;
- a private `.gitbay` hides the about from an outsider while the owner
  still sees it;
- dot-repos do not appear in `explore` or in a profile's repository
  list, and do appear in `repo list`;
- `profile set --about` is refused as an unknown flag.

e2e for the backfill in the shape of `e2e/commentmigrate_test.go`:
a row in the holding table becomes a repository with the file, and a
second run is a no-op.

`TestStdinCommandsReadStdin` already polices `profile set` dropping
`ReadsStdin`.

## Documentation

- `.gitbay/wiki/Parity` — the profile row.
- `.gitbay/wiki/Users` — the profile section: where the about lives and
  how to write it.
