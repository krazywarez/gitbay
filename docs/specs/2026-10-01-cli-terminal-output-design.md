# CLI terminal output

Status: accepted, 2026-10-01. Findings from a sweep of 51 read-only commands
against gitbay.org at a 110×50 colour terminal (`TERM=xterm-256color`),
plus error paths and piped output.

## How output works today

The CLI sends `--term=<cols>[,color]` as the first server argument when
stdout is a terminal. The server renders everything: `table` in
`internal/control/table.go` (header, padding, fit-to-width, relative
ages, colour on `kindState` cells), `view` in `view.go` (title line,
aligned fields, rendered body, events, comments), helpers in `term.go`.
Piped output is tab-separated rows with no header; `--json` is the
contract. Both are correct and stay unchanged.

The terminal mode exists but is thin. Colour applies only to state cells
whose word appears in `stateColor`'s fixed list, and to dim headers and
bold titles. Everything else renders as uncoloured text.

## Constraints

- Plain and `--json` output are byte-identical before and after, except
  for the bug fixes marked **(plain)** below.
- All rendering stays server-side in `internal/control`, so stock
  `ssh git@gitbay.org --term=120,color repo list` gets the same result
  as the CLI. No formatting in `cmd/gitbay`.
- Colour is never the only signal: every coloured cell keeps its word.
  `NO_COLOR`, `TERM=dumb`, `--no-color` keep working.
- Colour roles follow the stylesheet: green ok, magenta done, red bad,
  dim neutral, yellow for what wants the viewer (the web's orange),
  blue/cyan for refs (the web's accent, "what you can act on").

## Findings

### Renderer-wide

| # | Finding | Evidence |
|---|---|---|
| R1 | Only words in `stateColor`'s list are coloured. `public`/`private`, `verified`/`unsigned`, `full`/`runner` scopes, `admin`, `[archived]`, `primary` print plain. | `repo list`, `repo log`, `auth keys list`, `admin user list` |
| R2 | `kindRef` exists but is never painted; refs (`krz/gitbay#12`, `!546`, SHAs, branch names) look like prose. | every list |
| R3 | Future timestamps: `relAge` clamps negative durations to zero. Session expiry renders "until just now". | `web sessions list` |
| R4 | Some timestamp columns are `cText`, so they stay RFC3339 at a terminal. | `auth token list` EXPIRES |
| R5 | Durations in raw seconds. | `admin runners`: `wait avg 3223s`, `wait max 12886s` |
| R6 | Sizes in raw bytes where `admin stats` already humanizes. | `repo tree`, `release show` assets (`3177346`) |
| R7 | An optional trailing cell (`[archived]`, `primary`) adds an unnamed column: the header and every row get trailing whitespace, and the header has a blank title. | `repo list`, `auth email list` |
| R8 | A flex column that is blank on most rows still takes its maximum width. | `release list`: 90-cell TITLE column, blank on every visible row |
| R9 | Diffs are uncoloured. | `repo commit`, `mr diff`, `repo diff`, `mr range-diff` |
| R10 | Errors print as bare stderr lines with no visual distinction; an unknown flag gets no suggestion; list-command usage is a single 200-character line. | `issue list --stat open` |
| R11 | Section headings are inconsistent: `waiting on your review:` (lowercase, colon) on the dashboard; `link`/`org`/`repo` (singular) on `profile show`; `mirror` on `repo show`. The dashboard prints `none` under empty sections; other views skip them. | `dashboard`, `profile show`, `repo show` |

### Per command

| Command | Finding |
|---|---|
| `dashboard` | Empty sections ("waiting on your review: none") come first. No summary of what needs the viewer. A failing build looks the same as the other rows apart from its state word. Recent activity repeats `feed` (19 rows). |
| `mr list` | No draft marker, checks result, review state, mergeability or age, so the open-MR list cannot answer "what do I do next". |
| `issue list` | No labels, assignee, comment count or updated age. |
| `repo show` | The description renders as a bold title, and `public` has no colour. No clone URL, open issue/MR counts, latest release or last build. |
| `repo log` | Author as `(Name <email>)` takes a third of the width. No age column. Signature state is uncoloured, so an unsigned commit does not stand out. |
| `repo refs` | **(plain)** Tags sort lexically (`v1.10.0` before `v1.2.0`), oldest first; `gitutil.SortVersions` already does this for the web. The default branch is not marked. |
| `repo tree` | The SHA column comes first and is rarely wanted. Directories are not distinguished except by `/` and `-`. |
| `repo commit` | **(plain)** The subject prints twice: `message` is everything after the first blank line of the payload, which is the whole message. The signature state and checks are computed and included in the JSON but not printed. The date is RFC3339. |
| `release list` | No date. `10 asset(s)`. See R8. |
| `release show` | Byte sizes; full 64-hex SHA-256 per asset. |
| `label list` | Colour shown as hex text. A label with no colour leaves the cell blank. |
| `milestone list` | `due -` when there is no due date; progress as text. |
| `build show` | No steps, failed step, or pointer to `build log`. No link to the MR it belongs to. |
| `build log` | `$ step` lines are not distinguished from output. |
| `feed`, dashboard activity | Event sentences are uncoloured: `build success` and `build failure` read the same at a glance. |
| `audit` | DATA column is raw JSON. |
| `admin runners` | Fingerprints truncated (`SHA256:b5HGR…`) while `auth keys list` and `repo runner list` print them in full. |
| `auth keys list`, `web sessions list` | The key or session in use is not marked. |
| `auth whoami` | Prints the username only; at a terminal it could add instance, key label and scope. |
| `profile show` | The README heading repeats the tagline already on the title line. |
| mutations | `created krz/gitbay#312` with no URL; the output rules allow a second line for something to copy. |

Things that work and should stay: column fitting with `…`, relative
ages, `more: gitbay … --cursor` on stderr, pager for show/diff/log,
`nothing to list` on stderr, `mr show` checks sub-table, exit codes and
the not-found/usage messages.

## Plan

One issue per phase, one MR each, merged in order.

### Phase 1: renderer foundations (`term.go`, `table.go`, `view.go`)

1. Colour roles. Replace `stateColor`'s word switch with a role table
   (`ok`, `done`, `bad`, `neutral`, `warn`, `ref`) and add the missing
   words: visibility (`private` bad, `public` default), signature
   states from `internal/sig` (`verified` ok, `unsigned` and
   `signed_unknown_key` neutral, the other `signed_*` and
   `bad_signature` bad), `archived`, `primary`,
   `admin`, scopes. Add `sgrYellow`, `sgrCyan`.
2. Paint `kindRef` cells cyan (R2).
3. `relAge` handles the future: `in 5h`, `in 3w`, a date past 8 weeks
   (R3). Convert remaining timestamp `cText` cells to `cAge` (R4).
4. New cell kinds `cSize(bytes)` and `cDur(seconds)`; terminal renders
   `3.0 MiB` / `53m43s`, plain keeps the number (R5, R6).
5. Optional trailing cells move into a named column or merge into the
   state cell (`public, archived`); `join` never pads the last column
   and trims trailing spaces (R7).
6. A flex column blank on more than half its rows is capped at a third
   of the terminal width (R8).
7. One `section()` style: Title Case, bold, no colon, in every view;
   empty sections are skipped everywhere (R11).
8. Errors: at a terminal, `error:` prefix in red on stderr. Unknown
   flags get `did you mean --state?` from the registered usage (edit
   distance ≤ 2). Long usage wraps one alternative per line (R10).

9. `ParseTerm` reads `<cols>[,opt]...` and ignores options it does not
   know, so phase 4's capability tokens reach an instance that has
   phase 1 without turning its output plain.

Tests: `table_test.go` and `term_test.go` cases per kind and role;
plain output stays covered by the existing plain-mode table tests,
since every change above is gated on `Term.Cols`.

### Phase 2: diffs and logs

1. A `diffPaint` helper for unified diffs at a terminal: file headers
   bold, `@@` cyan, `+` green, `-` red, the stat block's `+`/`-` bars
   coloured. Used by `repo commit`, `repo diff`, `mr diff`,
   `mr range-diff`.
2. `repo commit` as a view: title line with short SHA, subject and
   signature state; fields for author, date (`when`), signer, checks;
   body; coloured diff. Fix the duplicated subject in plain output
   **(plain)**.
3. `build log`: `$ step` lines bold at a terminal; the failing step's
   line red when the build failed.

### Phase 3: action-oriented content

Each item adds columns or fields only at a terminal unless noted; plain
columns stay as they are.

1. `dashboard`: a first line summarising what needs the viewer
   (`2 need you: 1 review requested, 1 failing build`, yellow);
   non-empty action sections first (review requested, assigned, your
   failing builds), then open MRs/issues, pinned, builds; activity cut
   to 8 rows with a pointer to `gitbay feed`.
2. `mr list`: DRAFT marker in the state cell, CHECKS (`3/3`, red on a
   failure), REVIEW (`approved`, `changes requested`,
   `review requested` in yellow when it is the viewer), UPDATED age.
3. `issue list`: LABELS (first two, `+n`), ASSIGNEE, comment count,
   UPDATED age.
4. `repo show`: description as a body line rather than the title;
   fields for clone URL (ssh), open issues/MRs, latest release, last
   default-branch build with state.
5. `repo log`: AUTHOR as name only, an AGE column, signature coloured.
6. `repo refs`: `SortVersions`, newest first, default branch marked
   **(plain: order only)**.
7. `repo tree`: NAME first, directories cyan, SIZE humanized, SHA last.
8. `release list`: DATE column, `10 assets`; `release show`: sizes
   humanized, SHA-256 cut to 12 at a terminal.
9. `build show`: steps with state and duration, the failing step
   highlighted, `gitbay build log <n>` hint on stderr, the MR it
   belongs to.
10. `feed`/activity: colour the outcome word and refs.
11. `milestone list`: blank due instead of `due -`, progress as
    `1/2` plus a percentage.
12. `audit`: DATA rendered as `key=value` pairs at a terminal.
13. `auth keys list`, `web sessions list`: mark the current key or
    session with `*` (as `mr revisions` does).
14. `auth whoami`: at a terminal, add instance, key label and scope.
15. Mutations: a second line with the web URL for anything with a page
    (issue, MR, release, repo, snippet, wiki page).
16. `admin runners`: full fingerprints, consistent with the other lists.

### Phase 4: terminal capabilities

1. Capability tokens in `--term`: `truecolor` for label swatches
   (`●` in the label's colour), `links` for OSC 8 hyperlinks on refs.
   Relies on phase 1's `ParseTerm` change being deployed first.
2. Glyphs for compact columns (`✓ ✗ ●`), words kept everywhere else.

## Decisions (2026-10-01)

1. Yellow (`warn`) marks only what needs the viewer's action. `private`
   is red.
2. R8: a mostly-blank flex column is capped.
3. Phase 4 is in scope. Swatches go on when the client reports
   `COLORTERM=truecolor` or `24bit`; links go on when the client's
   terminal is known to support OSC 8 (`TERM_PROGRAM` in iTerm.app,
   WezTerm, ghostty, vscode; `KITTY_WINDOW_ID`; `VTE_VERSION` >= 5000)
   or `GITBAY_LINKS=1`, and off with `GITBAY_LINKS=0`.
4. Per-row data for `mr list` / `issue list` comes from one batch
   query per page in `internal/store`.
