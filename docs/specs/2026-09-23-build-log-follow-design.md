# Following a running build

Closes #250. `build log <owner/name> <n> --follow` streams a build's log
until the build reaches an outcome, and the web build page streams the
same command without JavaScript.

## Problem

No surface can follow a running build. `build log` prints what is stored
and exits; the build page renders the log once. Watching a build means
re-running the command or reloading the page.

## Decision

The capability is a flag on the existing control command. The web page
dispatches that command with a writer that escapes and flushes each
chunk, so the CLI, stock ssh and the page share one implementation.

Rejected: a `Refresh` header on the build page. It re-renders the page
and re-reads the whole log per reload per viewer, interrupts selection
and screen readers, needs an off switch for WCAG 2.2.1, and gives only
the web a way to follow.

## Store: waking followers

`Store` gains an in-memory waiter table keyed by build id:

- `BuildLogWait(id int64) <-chan struct{}` returns a channel closed by
  the next change to that build.
- `AppendBuildLog`, `FinishBuild` and `CancelBuild` close and drop the
  build's channel after their write succeeds (`wakeBuild(id)`).
- `BuildLogFrom(id, offset int64) (status string, chunk []byte, err
  error)` reads `status` and `substr(log, offset+1)` in one query, so a
  follower reads each byte once.

gitbayd is one process and its SSH and HTTP servers share one
`*store.Store`, so an in-memory table reaches every follower. Writers in
another process do not wake anyone: `gitbayd admin` subcommands, and
every session under the system-sshd forced command (`gitbayd shell`),
where each session is its own process. The follow loop also re-reads
every 2 seconds, which bounds that case.

## Command

`build log <owner/name> <n> [--follow]`, still `ReadOnly`.

Without `--follow`, unchanged.

With `--follow`:

1. Write the stored log.
2. Loop: take a wait channel, read from the offset, write any new bytes.
   If the status is no longer `pending` or `running` and the read
   returned nothing new, stop. Otherwise wait on the channel, the
   2-second timer, or `Ctx.Done`.
3. Write `build <n> <status>` to stderr and exit 0, whatever the
   outcome. Stdout stays the log, byte for byte.

The wait channel is taken before the read, so a change between the read
and the wait still wakes the loop.

`Ctx` gains `Done <-chan struct{}`, nil when the surface has none. The
embedded sshd closes it when the session's channel closes (the CLI's
shared connection outlives a Ctrl-C, the channel does not); `gitbayd
shell` passes nil, since its process ends with the session. httpd sets
it from `r.Context()` on the web and both API endpoints. A write
error also ends the loop. On `Done` the command returns
`protocol.ExitFailure` with no message; nobody is reading.

At most 8 follows per account run at once (a counter in `control`,
decremented on return). The ninth exits 4: "8 follows are already open
for this account; close one and retry". Signed-out web viewers are
account 0 and share the 8; the ninth gets the stored log once with the
refusal under it.

The CLI's `pass("log", …)` help in `cmd/gitbay/main.go` names
`--follow`.

## Web

`GET /{owner}/{repo}/builds/{n}` streams when the build is `pending` or
`running` and the query has no `follow=0`. Otherwise it renders as now.

Streaming:

1. Render `build.html` into a buffer with `Log` set to a marker and
   `Live` true, and split the output at the marker.
2. Write the head, flush.
3. Dispatch `build log <repo> <n> --follow` with `Stdout` an escaping
   writer (`template.HTMLEscape` per chunk, then flush through
   `http.ResponseController`) and `Done` from the request context.
4. After the command returns, read the build and write
   `<p class="notice" role="status">build finished: <status></p>` after
   the `</pre>` that begins the tail, then the rest of the tail. A
   dropped connection writes nothing more.

`build.html`, when `Live`, puts a line above the log: the log streams
until the build ends; a stream that stops with no "build finished" line
resumes on reload; and a link to `?follow=0`, the same page rendered
once, as the way to stop the updates (WCAG 2.2.2). The stored log
renders inside the stream from the first write, so there is no separate
"no log yet" state while live.

`gzipWriter` gains `Flush()` (flush the gzip stream, then the underlying
writer) and `Unwrap()`. The HTTP server has no `WriteTimeout`, so a long
silent step does not end the response. The handler sets
`X-Accel-Buffering: no` for a proxy in front of the instance.

## API

`/api/v1/cmd` and `/api/v1/read` buffer stdout, so there `--follow`
returns the whole log when the build ends. Streaming a JSON response is
out of scope.

## Tests

- `internal/store`: `BuildLogWait` closes on append, finish and cancel;
  `BuildLogFrom` returns bytes past the offset and the status.
- `internal/control`: follow a running build while another goroutine
  appends and finishes; stdout is the full log, stderr ends with the
  outcome, exit 0. Cancel ends a follow. A closed `Done` ends a follow.
  The ninth concurrent follow exits 4.
- `internal/httpd`: `gzipWriter` passes a flush through.
- `e2e`: ssh `build log --follow` on a running build while the runner
  key appends with `runner log` and reports with `runner done`; the web
  page of a running build arrives complete with "build finished:
  success"; `?follow=0` renders without the live line.

Docs: the CI wiki page and Parity get the flag.
