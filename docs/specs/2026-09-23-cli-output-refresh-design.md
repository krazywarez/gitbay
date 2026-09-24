# CLI output refresh

Ref #254. Terminal rendering for tables, `show` views and help, with
piped output unchanged in shape, plus the fixes from the 2026-09-23 CLI
audit.

## Problem

The plain output has had no design pass since v1.22.0.

- Tables have no header, no colour and no width limit. The CLI pads
  tabs with `tabwriter` for the verbs in `listVerbs`
  (`cmd/gitbay/ssh.go`) and nothing else.
- `show` views print stored markup verbatim: event lines read
  `referenced in commit [6c4d1e1454](/krz/gitbay/commit/…) by [cmc](/cmc)`,
  and timestamps are whatever string the row holds
  (`2026-09-23T23:26:00.570Z`). The web prints `2026-09-23 23:26 UTC`.
- Root help carries usage in the summary column. Noun help is two
  lines per verb with the full usage. Verb help is the same two lines;
  no flag has a description, and no command has an example.
- `pass()` in `cmd/gitbay/main.go` holds a second copy of each usage
  string, which drifts (`build list` still reads
  `recent builds: <owner/name>`).

## Decision

The server renders. The CLI tells it the terminal's width and whether
colour is wanted; the command's plain formatter chooses between
terminal and plain rendering. The CLI adds a pager for long views and
the grouped root help, and nothing else.

Rejected:

- The CLI rendering from `--json`. A second renderer per command in
  `cmd/gitbay`, which drifts from the server's, and stock ssh never
  sees the result.
- A structured view format on the wire (typed columns, a document
  tree) styled by the CLI. A new protocol between two programs that
  ship together, for no visible gain over rendering on the server.

## Transport

The CLI passes `-o SetEnv=GITBAY_TERM=<cols>[,color]` when stdout is a
terminal. `color` is left out when `NO_COLOR` is set and non-empty,
`TERM` is `dumb`, or `--no-color` is given (stripped by the CLI before
dispatch). Piped stdout sends nothing.

sshd accepts an `env` request named `GITBAY_TERM` and ignores every
other name, as today. `runExec` parses the value into `Ctx.Term`:

```go
type Term struct {
	Cols  int  // 0: plain output
	Color bool
}
```

A missing or malformed value, or `Cols` under 40, is the zero value.
The HTTP surfaces never set it. Under the system-sshd forced command
(`gitbayd shell`) the variable arrives only if the operator adds
`AcceptEnv GITBAY_TERM`; without it output is plain. The Admin wiki
page says so.

`SetEnv` needs OpenSSH 7.8. The CLI shares one connection through
ControlMaster; the first task verifies that a multiplexed session
carries `SetEnv`. If it does not, the CLI sends a leading
`--term=<cols>[,color]` argument instead, and `Dispatch` strips it
wherever it appears, as it strips `--json`.

Stock ssh users opt in with `ssh -o SetEnv=GITBAY_TERM=120,color`.

`alignColumns`, `listVerbs` and the `tabwriter` in `runSSH` are
removed.

## Tables

`internal/control/table.go`:

```go
t := c.table("#", "STATE", "TITLE", "AUTHOR", "UPDATED")
for _, is := range issues {
	t.row(ref("#", is.Number), state(is.State), text(is.Title), text(is.Author), age(is.UpdatedAt))
}
t.flush()
```

Cells are typed: `ref`, `state`, `text`, `flex`, `age`, `num`. The
`flex` column (a title or description, one per table) is the one that
shrinks first.

Plain (`Term.Cols == 0`): one row per item, cells joined by tabs, no
header, `age` as RFC3339 to the second in UTC (`2026-09-23T23:26:00Z`).
This is the current shape; only the timestamp format changes.

Terminal:

- Header row in capitals, dim when `Color`.
- Columns padded with two spaces between them.
- Width: when a row exceeds `Cols`, the flexible column is cut with
  `…`, down to 8 cells. If that is not enough, the other `text`
  columns are cut from the right. `ref`, `state`, `age` and `num`
  are never cut. Widths are measured in display cells
  (`golang.org/x/text/width`), not bytes.
- `age` is relative: `just now` under a minute, then `5m ago`,
  `2h ago`, `3d ago` under 14 days, `3w ago` under 8 weeks, then
  `2006-01-02`.
- `state` colour, ANSI 16-colour so the terminal's theme sets the
  shade, matching the web's state tokens: green (`--ok`) for `open`,
  `success`, `approved`; magenta (`--done`) for `merged`; red (`--bad`)
  for `failed`, `error`, `changes requested`; dim (`--neutral`) for
  `closed`, `draft`, `pending`, `canceled`. Anything else is
  uncoloured.

An empty table prints `nothing to list` on stderr, as `emit` does now.

`emitPage` in terminal mode prints the next cursor on stderr as
`more: gitbay <path> <args> --cursor <c>`. Plain mode keeps the
`next\t<c>` row.

Every list command moves to `table`: each `emit`/`emitPage` whose
plain formatter prints one row per item. Mutations and single-value
reads keep their one line.

## Show views

`internal/termtext` renders markdown (goldmark) and org (go-org) syntax
trees to terminal text at a given width:

- Paragraphs wrapped at `Cols - 2`, indented two spaces.
- Headings bold; list items with `•` or their number, continuation
  lines hanging.
- Code blocks indented four spaces, not wrapped, highlighted with
  chroma's `terminal16` formatter when `Color`.
- Emphasis bold or underlined when `Color`, plain text otherwise.
- Links as their text, followed by ` (<url>)` when the URL differs
  from the text. Relative forge links are made absolute from
  `server.site_url`.
- Images as `[image: <alt>]`.

The same package has a plain mode: no ANSI, no wrapping, links
reduced as above.

A `view` helper in `internal/control` lays out every `show`:

```
#253  Recorded CLI walkthrough on the landing page          closed

  author     cmc, 2026-09-22 18:04 UTC
  closed     2026-09-23 23:26 UTC by cmc in fc2380f3f0
  labels     docs, web
  milestone  v1.35.0
  url        https://gitbay.org/krz/gitbay/issues/253

  <rendered body>

  · cmc referenced this in 6c4d1e1454                 2026-09-23 23:26 UTC
  · cmc closed this in fc2380f3f0                     2026-09-23 23:26 UTC

── cmc, 2026-09-23 23:40 UTC ──────────────────────────────────────
  <rendered comment>
```

Title bold and state coloured when `Color`; events dim. Timestamps in
views use the web's format, `2006-01-02 15:04 UTC`. Plain mode prints
the same lines without colour or wrapping, with RFC3339 timestamps.
Commands: `issue show`, `mr show`, `build show`, `release show`,
`snippet show`, `repo show`, `milestone show`, `org show`,
`profile show`, and any other `show` whose output has a body or more
than one field.

### Pager

When stdout is a terminal, the CLI runs `show`, `diff` and `log`
verbs through a pager: `$GITBAY_PAGER`, else `$PAGER`, else
`less -FRX`. `-F` exits when the output fits one screen. An empty
`GITBAY_PAGER` disables it. `build log --follow` is never paged. The
pager's exit does not change the command's exit code.

## Help

`Command` gains:

```go
type Flag struct {
	Name    string // "--state"
	Arg     string // "open|closed|all", empty for a switch
	Desc    string // "which issues"
	Default string // "open", empty for none
}

Flags    []Flag
Examples []string // full argv after the program, owner/name explicit
```

`help --json` adds `flags` and `examples` to each entry.

Server `help` with a prefix, in terminal mode:

- Noun (a prefix matching more than one command): the noun's summary,
  `USAGE  gitbay <noun> <verb> [<owner/name>] ...`, then `READ` and
  `WRITE` sections from `ReadOnly`, one line per verb with its
  summary, then `gitbay <noun> <verb> --help for flags.`
- Verb (a prefix matching one command): summary, `USAGE`, `FLAGS`
  one per line with description and `(default …)`, `--json` last,
  then `EXAMPLES`.

Examples print with a `gitbay ` prefix in terminal mode and
`ssh <ssh host> ` in plain mode; both are valid because each example
names its repository.

Plain mode without a prefix stays one line per command. With a prefix,
plain mode prints the same sections as terminal mode without colour.

The CLI's noun and verb `--help` already go to the server. `pass()`
drops its short text; the cobra command takes its one-line summary
from a table generated from the registry (`go generate`, checked by a
test that the file is current), so root and completion text never
drift.

Root help stays local to the CLI and is grouped:

```
WORK          issue mr build release milestone label search
REPOSITORIES  repo wiki status webhook init
YOU           dashboard feed notifications auth profile snippet web
INSTANCE      org explore register migrate remote admin audit
```

one noun per line with its summary. A test fails when a top-level
command is in no section or two.

Registry test: every `--flag` token in `Usage` has a `Flags` entry
and every `Flags` entry appears in `Usage`; every command has a
non-empty `Desc` per flag and at least one example; every example
resolves through `Lookup` to its own command.

## Audit fixes

- `release list` takes `--limit`/`--cursor`; the title column is
  empty when the title equals the tag.
- `notifications device add` prints `registered device <n>`.
- `dashboard` prints `none` under an empty section.

Two audit findings need no change. `build log --follow` already
says what to do when it gives up on a queued build
(`buildfollow.go`). A missing positional through `c.usage()` already
prints the registered usage and exits 2, which is the rule; converting
198 call sites to `usageWith` for an extra line is not worth the
churn.
- The stale `build list` help goes with the `pass()` short text.

## Rules

The Users wiki "Output rules" gains an "At a terminal" section:
headers, colour, width, relative ages, the pager, `GITBAY_TERM`,
`NO_COLOR`. The piped rules stand, with timestamps stated as RFC3339
to the second.

## Testing

- `table`: plain rows byte-identical to the current formatter for a
  fixture per cell type; terminal golden files at 60 and 120 columns,
  with and without colour; wide-character titles.
- `termtext`: golden files for markdown and org fixtures covering
  every node type above, at 60 columns, plain and colour.
- `Term` parsing: valid, malformed, under 40 columns, absent.
- e2e: every `ReadOnly` command in `readArgs`
  (`e2e/readonly_test.go`) run twice through ssh: plain, asserting no
  `\x1b` in stdout; with `GITBAY_TERM=60,color`, asserting no line
  wider than 60 display cells after stripping ANSI, code blocks
  excepted.
- CLI: `SetEnv` sent only when stdout is a terminal; `NO_COLOR`,
  `TERM=dumb` and `--no-color` drop `color`; pager selection order.

## Delivery

Five MRs, each shippable alone:

1. Audit fixes.
2. Transport, `Term`, `table`, and every list command.
3. `termtext`, `view`, every show command, the pager.
4. `Flags`/`Examples` for every command, help layouts, the generated
   summary table, grouped root help.
5. Users and Admin wiki pages, CHANGELOG.
