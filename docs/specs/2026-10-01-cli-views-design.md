# CLI views

Status: accepted, 2026-10-01. Follows `2026-10-01-cli-terminal-output-design.md`
(v1.41.0, #312–#315).

## Problem

Terminal output after v1.41.0 is coloured and humanized but still noisy.
Captured at 110 columns against gitbay.org:

- Every list and every dashboard section is the same grid under a dim
  ALL-CAPS header row. There is no hierarchy; a header row costs a line
  and says little (`#`, `!`, `REF`, `WHEN`).
- Cyan marks every ref, path, SHA and feed target. A `build list` row has
  three cyan cells, so cyan carries no information.
- `dashboard` runs to about 70 lines: 20 builds, 7 pinned repositories,
  8 activity rows, and the admin `Server`/`Queues` blocks. `Queues` uses
  raw tabs, and its nested tables are padded inside the colour codes, so
  they do not align with the parent.
- `krz/gitbay` repeats on 15 of 20 build rows; refs keep their owner
  prefix inside the repository they belong to.
- State reads three ways: `✓ 2/2` on `repo show`, coloured words on
  `build list`, an uncoloured `running`.
- Nothing says what to run next, apart from a `more:` line that repeats
  the command with an opaque cursor.

The design bar is magit's status buffer: a header block of `Label:` lines,
counted section headings, short dim refs on the left, and a menu of the
actions that apply right now.

## Constraints

- Piped output and `--json` are byte-identical before and after.
- Rendering stays server-side in `internal/control` behind `--term`, so
  stock `ssh` with `--term=<cols>,color` gets the same screens as the CLI.
  No formatting moves into `cmd/gitbay`.
- Colour is never the only signal. `NO_COLOR`, `--no-color` and
  `TERM=dumb` drop colour and keep glyphs and layout.
- Diffs, `build log` and blobs are streams, not screens. They keep the
  v1.41.0 rendering; only their header lines change.

## Visual rules

A screen has up to four parts, in order, each separated by one blank line:
a header block, a body, sections, and an action legend. Sections are
separated from each other by one blank line.

**Header block.** Aligned `Label:` lines. Labels are dim and padded to the
longest label in the block; values are normal weight. The first line names
the object: `Merge:  !552  wire $PAGER through long views`.

**Body.** Markup text (an issue or MR description, release notes), rendered
through `termtext` as `view.body` renders it today.

**Section heading.** `Title (n)`, bold blue. `n` is the total, not the
number of rows shown. An empty section is omitted unless the screen marks
it as worth showing empty (`Discussion (0)`).

**Rows.** No header row, no indent. Column order: ref, glyph, text,
metadata.

- Ref: the short form (`!549`, `#12`, `8f3a1c2`, `1779`), dim,
  left-aligned. The owner prefix is dropped when the screen already names
  the repository.
- Text: normal weight. The title column keeps the existing one-third-width
  cap for sparse columns.
- Metadata: dim, after the text, joined by ` · `.
- A section capped by its screen ends with a dim
  `+12 more  gitbay build list`.

**Colour.** Follows the stylesheet's two accents.

| Role | Colour | Used for |
|---|---|---|
| What you can do | blue | section headings, legend commands |
| What wants you | yellow (`--warn` under truecolor) | review requested, assigned to you, behind, needs approval |
| State | green / red / none | passed, open, signed / failed, blocked / finished, neutral |

Nothing else is coloured. Refs and paths lose their cyan.

**Glyphs.** First in a row or field, one meaning each, everywhere:

| Glyph | Meaning |
|---|---|
| `✓` | passed, ok |
| `✗` | failed, blocked |
| `◐` | running, pending |
| `●` | waiting on you |
| `○` | closed, draft |

**Legend.** A rule line, then up to three columns of action groups. Each
group is a bold name over its commands, blue. Below 80 columns the groups
stack in one column. Commands omit `<owner/name>` when it equals the repository the CLI
inferred from its clone, which the CLI sends as `here=<owner/name>` in
`--term` (an older server ignores the option). Commands may name
CLI-local commands (`mr rebase`, `mr checkout`). There is no `browse`:
the header's first field links to the page where the terminal shows
links, and a `URL:` field carries it otherwise.

## Model

The existing `view` (`internal/control/view.go`) is an imperative writer
used by 18 files; it renders both piped and terminal output, branching on
`Term.Cols == 0`. It stays as the plain writer. The declarative model is a
new type, `screen`, in `internal/control/screen.go`:

```go
type screen struct {
	fields   []field
	body     string // markup source
	format   string // body's markup format
	sections []section
	actions  []action
}

type field struct {
	label string
	value []cell
}

type section struct {
	title string
	n     int    // total, shown as "(n)"
	rows  []row    // cells, plus a markup body drawn beneath (a comment)
	more  []string // command for the rest, when n > len(rows)
	empty bool   // draw "(0)" rather than omit
}

type row struct {
	cells  []cell
	body   string
	format string
}

type action struct {
	group string
	argv  []string
}
```

Rows use the existing typed cells (`cRef`, `cAge`, `cSize`, `cSwatch`,
...) plus a new `cGlyph(state)`.

A migrated command calls

```go
c.emitView(data, plain, func() screen { ... })
```

Under `--json` it encodes `data`; piped, it runs `plain`; at a terminal it
builds the screen and renders it. `emitPage` gains the same form; at a
terminal its cursor becomes a final `Next page` action carrying the full
command, replacing the `more:` line.

The width logic in `table.flush` (`fit`, `capSparse`, `dropEmpty`) moves
into the section renderer. At a terminal, `table` renders as one untitled
section, so lists that have not migrated take the new row style in stage 1. Such a
table keeps a dim header row only when a column is a number or size
(`admin runners`, `admin stats`), which is unreadable without one.
Piped `table` output is unchanged. `view`'s terminal branches are removed
once no command reaches them.

Actions are data. Each command chooses them from the state it just read:
behind the target offers `mr rebase`, a requested reviewer gets
`mr approve`, `mr merge` appears only when the merge gates pass, a caller
without write access gets no `issue close`. The renderer never fails a
command; a field without data is omitted.

## Screens (stage 2)

`--json` for each is unchanged.

**`dashboard`**

- Header: `User`, `Instance`. For an admin, an `Instance` problem line
  (`✗ 1 mirror error`) when one exists. `Server` and `Queues` leave the
  terminal screen; `admin stats` carries them.
- Sections: `Review requested`, `Assigned issues`, `Your merge requests`,
  `Failed builds` (last 24h, repositories the viewer can write),
  `Recent activity` (5), `Pinned` (refs on one line). Passing builds
  collapse to one dim line under activity: `14 builds passed today`.
- Legend: the command for the first item of each non-empty section,
  `feed`, `build list`, and `admin stats` for an admin.

**`mr show`**

- Header: `Merge`, `State`, `Checks`, `Review`, `Gates` (each
  `MergeGates` gate as a glyph).
- Body: description.
- Sections: `Commits` (SHA, subject), `Files` with `+a −d`,
  `Discussion` (each comment as who · age with its body beneath; shown
  when empty).
- Legend: Unblock (`mr rebase` when behind), Review (`mr review
  --approve`, `mr comment`), Merge (when the gates pass), Read
  (`mr diff`).

**`issue show`**

- Header: `Issue`, `State`, `Labels` (swatches), `Assignee`,
  `Milestone`, `Linked` (merge requests that close it).
- Body: description.
- Sections: `Discussion` as on `mr show`.
- Legend: comment, assign, label, close or reopen, filtered by permission.

**`repo show`**

- Header: `Repo`, `Clone`, `Head` (`main ✓ 2/2`), `Release`, `Mirror`
  (only on error).
- Body: description and topics.
- Sections: `Open merge requests`, `Open issues`, `Recent commits` (5).
- Legend: `mr create`, `issue create`, `repo log`.

**`build show`**

- Header: `Build`, `State`, `Commit`, `Ref`, `MR`, `Duration`.
- Sections: `Steps` (glyph, name, duration; the failed step red).
- Legend: `build log`.

**`mr list`, `issue list`, `build list`**

- One section each (`Open merge requests (n)`, ...).
- Legend: `create`, the state filter not in use, `Next page`.

## Stages

Each stage is tracked by its own issue under one tracking issue, and lands
as merge requests in order.

1. `screen.go`, the renderer, `emitView`, `cGlyph`; `table` rendered as an
   untitled section. Rewrite the Users wiki "Output rules" section.
2. The stage-2 screens: one MR for the show screens, one for the lists and
   `dashboard`.
3. Every other `c.table` and `c.view` caller, file by file, starting with
   `profile`, `release`, `milestone`, `org`, `label`. Remove `view`'s
   terminal branches.

Out of scope: a branch-aware `gitbay status` (needs the CLI to send the
local branch), interactive input of any kind, and Emacs integration.

## Testing

- **Renderer goldens** (`screen_test.go`) at 80 and 120 columns, colour on
  and off. Every golden asserts that `stripSGR` of the colour render equals the colourless render, so
  colour never carries structure alone.
- **Screens as data.** Per-command tests assert on the `screen` value, not
  rendered text: sections present, counts, which actions are offered for a
  given state and caller.
- **Legend commands resolve.** A test passes every action argv that any
  screen produces in its fixtures through the registry's `Lookup` and
  `parseFlags`. A renamed command or flag fails CI.
- **Plain output pinned.** Before a command migrates, its piped output is
  captured as a golden; the migration MR reproduces it byte for byte.
  `--json` stays covered by the existing tests.

## Compatibility

All changes are server-side behind `--term`. Any CLI from v1.41.0 gets the
new screens when the instance upgrades; older CLIs that send no `--term`
get plain output as before. Scripts see no change.
