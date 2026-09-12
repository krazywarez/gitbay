# Snippets

Closes #195 (ref #185). A snippet is a small set of named text files a
user owns, shares by URL, and edits in place: paste.sr.ht's paste with a
gist's mutability, without the git repository underneath.

## Problem

#185 walked a sourcehut user's day and found that sharing a log or a
fragment several times a week has no object here. Neither a repository
(too heavy for a log) nor an issue (wrong shape) covers it. #195 asks for
the feature rather than a FAQ entry declining it.

## Decision

A snippet is rows in SQLite: an owner, a description, a visibility, and
one or more named files with their content. It is created and edited
over SSH and the API, rendered and edited on the web through the same
control commands, and identified by an opaque id in a URL under the
owner.

Decisions taken on the way, with the alternatives rejected:

- **Store-backed, not a git repository.** A gist is a bare repository
  with a flag, cloneable and versioned, which would drag in a namespace
  decision, a receive-pack path that skips CI, issues and merge
  requests, and the whole repository policy surface. The use case is a
  log pasted from a terminal. If history is ever wanted the id and URL
  scheme below do not have to change.
- **Mutable, opaque id.** paste.sr.ht keys a paste on a hash of its
  content, so a typo fix changes the URL already shared. A random id
  keeps the URL; updating a file replaces it and no history is kept.
- **Three visibilities, default unlisted.** `public` is listed on the
  owner's page; `unlisted` is readable by anyone with the URL and listed
  nowhere; `private` is the owner's alone and answers 404 to everyone
  else, as a private repository does. Sharing a log wants unlisted,
  so that is the default when `snippet create` names none.
- **Users only.** Nothing in #195 or #185 asks for an org to own a
  snippet, and an org snippet would need a membership rule for writes.
- **Web writes in the same merge request.** The Parity rule lands only
  the triage loop on the web at once; the request here was parity in one
  go. Every form dispatches a control command through `runControlStdin`,
  so no rule lives in a handler.
- **Text only.** Content must be valid UTF-8. A snippet is read on a
  page and served raw as `text/plain`; a binary belongs in a release
  asset.
- **One file per command.** Stock OpenSSH carries one stdin stream, so
  `snippet create` takes one file and `snippet file set` adds the rest.
  A packed multi-file format on stdin would fail the stock-ssh
  constraint.
- **No `--yes` on delete.** `release delete --yes` guards assets that
  are gone for good; a snippet has nothing hanging off it and is a
  paste, not a release.
- **No events, audit rows, notifications, comments, search, Atom feed
  or explore listing.** Events are keyed on a repository. None of the
  rest was asked for.

## Data

Migration 0053, no rebuild:

```sql
CREATE TABLE snippets (
    id          INTEGER PRIMARY KEY,
    public_id   TEXT NOT NULL UNIQUE,
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    description TEXT NOT NULL DEFAULT '',
    visibility  TEXT NOT NULL CHECK (visibility IN ('public','unlisted','private')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX snippets_owner ON snippets(owner_id, id);

CREATE TABLE snippet_files (
    snippet_id INTEGER NOT NULL REFERENCES snippets(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    content    BLOB NOT NULL,
    size       INTEGER NOT NULL,
    PRIMARY KEY (snippet_id, name)
);
```

`public_id` is 12 lowercase hex characters from `crypto/rand` (48 bits),
generated on create and retried on a unique violation. It is global: the
CLI takes `<id>` alone, and the owner in the URL is for reading, not for
lookup.

`updated_at` moves on every file or metadata write. Deleting a user
deletes their snippets through the cascade, as it does their keys.

Limits:

- `limits.max_snippet_bytes` in `config.toml`, per file, default
  1 MiB. Enforced on create and `file set` with the same
  `io.LimitReader(n+1)` shape as `release asset add`.
- 64 files per snippet, a constant in `internal/control/snippet.go`.
- `limits.max_snippets_per_user`, default unlimited, like
  `max_repos_per_user`. Enforced on `snippet create`; admins are not
  exempt.
- Snippet bytes do not count toward `max_bytes_per_user`; that quota
  measures repositories and LFS.

File names match the release asset name pattern:
`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`, so no slashes and no leading dot.

`store.Snippet` carries the row plus `OwnerName`; `store.SnippetFile`
carries `Name`, `Size` and `Content`. Store functions: `CreateSnippet`,
`SnippetByPublicID`, `ListSnippets(ownerID, all bool, limit, cursor)`,
`UpdateSnippet`, `DeleteSnippet`, `SetSnippetFile`, `RemoveSnippetFile`,
`SnippetFiles`, `SnippetFile`. Hand-written SQL, as everywhere.

## Commands

All in `internal/control/snippet.go`, registered like every other noun.
Reads set `ReadOnly`; the two commands that take a body set
`ReadsStdin`; nothing is `SSHOnly`, so the JSON API reaches all of it.

| command | usage |
|---|---|
| `snippet create` | `snippet create <filename> [--description <d>] [--visibility public\|unlisted\|private] < file` |
| `snippet show` | `snippet show <id>` |
| `snippet list` | `snippet list [<owner>] [--limit n] [--cursor c]` |
| `snippet edit` | `snippet edit <id> [--description <d>] [--visibility <v>]` |
| `snippet delete` | `snippet delete <id>` |
| `snippet file set` | `snippet file set <id> <filename> < file` |
| `snippet file get` | `snippet file get <id> <filename> > file` |
| `snippet file remove` | `snippet file remove <id> <filename>` |

Behaviour:

- `create` reads stdin as the first file, refuses empty stdin, content
  that is not valid UTF-8, or content over the limit (exit 2 with the
  reason), and prints the id and the web URL. JSON: `{id, url, owner,
  description, visibility, created_at, updated_at, files:[{name,size}]}`.
- `show` prints the metadata and the file list with sizes. `--json`
  includes each file's `content` so one API read returns the whole
  snippet.
- `list` with no argument lists the caller's snippets at every
  visibility, newest first. With `<owner>` it lists that owner's public
  snippets, or everything when the owner is the caller or an admin. An
  unknown owner is exit 3. Paged with keyset cursors like `repo list`;
  the JSON is `{items, next}`.
- `edit` changes the description or the visibility, or both; neither
  given is exit 2.
- `file set` adds a file or replaces one by name, under the same checks
  as `create`, and refuses the 65th file. `file remove` refuses to
  remove the last file: a snippet always has one. `file get` writes the
  content to stdout unchanged.
- `delete` removes the snippet and its files.

Access, in `internal/policy` beside the repository rules. Key scope needs
no rule of its own: the dispatcher already refuses every control command
to a key that is not `full`.

- Read: the owner and admins for `private`; anyone, anonymous included,
  for `unlisted` and `public`.
- Write: the owner and admins.
- A snippet the caller may not read is exit 3, never 4, so a private id
  cannot be confirmed. A snippet the caller may read but not write is
  exit 4.

CLI: one `pass()` per command in `cmd/gitbay/main.go`. `snippet create`
and `snippet file set` use `alwaysStdin` with a `stdinWhat` naming the
file's bytes, as `release asset add` does. No `needsRepo`: the noun is
not repository-scoped, and the repo argument is never inferred.

## Web

Routes live under `/{owner}/-/`, the pattern `/{owner}/-/labels` set: a
hyphen cannot start a repository name, so nothing is shadowed and no
word joins `internal/policy/names.go`.

| method | path | handler |
|---|---|---|
| GET | `/{owner}/-/snippets` | list: the owner's public snippets; everything, marked by visibility, when the viewer is the owner or an admin |
| GET | `/{owner}/-/snippets/new` | create form, `requireUser`, owner must be the viewer |
| POST | `/{owner}/-/snippets/new` | dispatch `snippet create`, redirect to the snippet |
| GET | `/{owner}/-/snippets/{id}` | the snippet: description, visibility, each file highlighted with a raw link |
| GET | `/{owner}/-/snippets/{id}/raw/{name}` | `text/plain; charset=utf-8`, `X-Content-Type-Options: nosniff` |
| POST | `/{owner}/-/snippets/{id}/edit` | dispatch `snippet edit` |
| POST | `/{owner}/-/snippets/{id}/delete` | dispatch `snippet delete`, redirect to the list |
| POST | `/{owner}/-/snippets/{id}/file` | dispatch `snippet file set` with the textarea on stdin |
| POST | `/{owner}/-/snippets/{id}/file/remove` | dispatch `snippet file remove` |

An id under the wrong owner is 404. Private snippets are 404 to anyone
but the owner and admins, unlisted ones render for anyone with the URL.
Files are rendered through the existing `highlight(path, data)` by
extension; `.md` and `.org` are highlighted as source, not rendered as
markup, since a snippet is a paste rather than a document. Each file
heading links to its raw route.

Forms are on the snippet page for the owner: a textarea per file posting
`file`, a remove button per file, an add-file form (name and textarea),
a description and visibility form, and delete. All POSTs go through
`checkOrigin` and `requireUser`, and answer through `done`, so a refusal
returns to the page with the message and a missing snippet is the 404
page.

The owner page shows a `snippets` link below the repository list
when the owner has a public snippet, or when the viewer is the owner.

Templates: `snippets.html`, `snippet.html`, `snippetnew.html`. Stylesheet
additions in `static/style.css` only where an existing class does not
fit; the file blocks reuse the blob page's classes.

## Testing

`e2e/snippet_test.go`, over ssh with the real binary:

- create prints an id of 12 hex characters and a URL; `show` and `file
  get` round-trip the content byte for byte; `--json` on `show` carries
  `content`.
- visibility, from a second account and from anonymous HTTP: private is
  exit 3 and 404 to the other account, unlisted is readable by id and
  absent from `snippet list <owner>`, public is listed; the owner's own
  `snippet list` shows all three.
- `file set` replaces, `file remove` refuses the last file, the 65th
  file is refused, non-UTF-8 and oversize bodies are refused with exit 2.
- `edit` moves visibility and the listing follows.
- `delete` then `show` is exit 3; deleting the user cascades the rows.
- the other account cannot `edit`, `file set` or `delete` (exit 4 on an
  unlisted snippet, exit 3 on a private one).

`e2e/snippetweb_test.go`: the list and snippet pages render, raw is
`text/plain` with nosniff, the create form makes a snippet, the file and
edit forms change it, delete removes it, and the owner page carries the
link. The existing coverage tests hold the rest: the CLI table, the
`ReadsStdin` flag, and that every `ReadOnly` command writes nothing.

## Documentation

- `.gitbay/wiki/Users.org`: a `* Snippets` section after Pages.
- `.gitbay/wiki/Parity.org`: rows for `snippet create, edit, delete`,
  `snippet show, list`, `snippet file set, get, remove`; `cli` yes, `web`
  yes, `ios` no.
- `CHANGELOG.org`: the v1.20.0 entry.
- `.gitbay/wiki/Admin.org`: `max_snippet_bytes` beside `max_asset_bytes`
  in the limits list.
