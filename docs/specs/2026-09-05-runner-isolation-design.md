# Runner isolation with rootless podman

Ref #144. Milestone v1.14.0. Splits from #115, whose concurrency half (`-jobs N`)
is done.

## Problem

A build runs whatever a repository's `.gitbay/ci.yml` says, through `sh -c`, as
the runner process's own user (`cmd/gitbay-runner/main.go:309`).

`deploy/gitbay-runner.override.conf` constrains the *service* —
`NoNewPrivileges`, `ProtectSystem=full`, `RestrictSUIDSGID`, CPU and IO weight —
and the runner's key carries scope `runner`, so a build cannot administer the
instance. Three things it can still do:

- **Read the runner's SSH private key.** It is on disk, owned by `ci-runner`,
  and a step runs as `ci-runner`. Scope `runner` is not nothing: it claims
  builds, posts logs, and marks builds done, for every repository the runner
  serves.
- **Read the runner's environment.** `cmd.Env = append(os.Environ(), …)`
  (`main.go:311`) hands each step the runner's entire environment, not a
  constructed one.
- **See other builds in flight.** `-jobs N` puts N workspaces under one
  `-workdir`, all readable by the same user.

Acceptable while every repository on the instance is the operator's. It stops
being acceptable the moment a fork's CI runs, which `registration = "open"`
makes reachable.

## Decision

Rootless podman, chosen over bubblewrap and a hand-rolled `unshare` sandbox.

The deciding factor was not isolation strength — bubblewrap would have been
enough for that, at one small package with no daemon. It was `image:` per job.
An OCI runtime is the only option that gets there, and a CI system where every
build runs against whatever the host happens to have installed is a CI system
people work around rather than with.

The cost is real and should be stated rather than discovered: podman is a
substantially larger dependency on bay1 than anything the runner has needed so
far, it needs subuid/subgid delegation for `ci-runner`, and it introduces image
storage that grows without pruning. The runner stops being a single Go binary
plus `git`.

## Design

### What runs where

The split matters more than the flags:

- **The runner clones**, outside any container, using its own key. The container
  never sees `GIT_SSH_COMMAND`, the key, or the runner's environment.
- **The container runs the steps**, with the already-cloned workspace bind
  mounted read-write at a fixed path.

That alone closes the key-theft path, independently of how good the sandbox is.

### Per-step or per-job

Per job, one container for all of a job's steps. Steps in a job share state
today — a build step writes what a test step reads — and per-step containers
would break that or force a layer-caching scheme nobody asked for. `sh -c` per
step stays, inside the one container.

### The environment

Stop inheriting. Build the step environment explicitly: `PATH`, `HOME`, `CI`,
the build's own variables, and secrets when `b.Trusted`. `os.Environ()` must not
appear in the container's environment.

Secrets stay env vars inside the container. They are visible to `podman inspect`
on the host, which is the runner's own user — the same user that already holds
them in memory, so this is not a new exposure. It is worth a comment saying so,
because it looks like one.

### `image:` in ci.yml

`Job` gains `Image string`. Empty means the instance default, which is
configuration on the runner (`-image`), not a value baked into the binary.

Validation belongs in `ci.Parse` beside the existing job checks: a reference,
not a command line. Reject anything with a shell metacharacter or whitespace —
this string reaches `podman run`, and the whole point is that a repository's
config file cannot become an argument injection.

### Network

Default on. A build that cannot fetch dependencies is useless to most projects,
and this design's threat model is about what a build can *reach on the host*,
not about exfiltration. `network: none` per job is a plausible later addition
and explicitly out of scope here.

### Failure modes

**Podman missing or broken.** The runner must refuse to run builds rather than
falling back to running them unsandboxed. A fallback that silently drops
isolation is worse than a stopped runner, because nothing surfaces it. Check at
startup, fail loudly, and say what is wrong.

**Image pull failure.** Fail the build with the pull error in the log. Do not
retry indefinitely; do not fall back to another image.

**Storage growth.** Images accumulate. Prune on a schedule, and document it —
an unbounded cache on a 40GB host is a slow outage.

### Host changes on bay1

- `apt install podman` (Debian 13 has it; kernel 6.12, cgroup2, user namespaces
  enabled with `max_user_namespaces` = 31556 — all already verified).
- `/etc/subuid` and `/etc/subgid` entries for `ci-runner`.
- The systemd override needs `Delegate=yes` for rootless cgroup management, and
  `ProtectSystem=full` must be checked against podman's storage under
  `~/.local/share/containers` — it may need an explicit `ReadWritePaths`.

These are deployment changes, not code, and belong in `deploy/` with the rest.

## Sequencing

Three merge requests, because the middle one is the risky one and should be
reviewable on its own:

1. **Environment hygiene.** Stop inheriting `os.Environ()`; construct the step
   environment explicitly. Independently valuable, no new dependency, and
   testable today.
2. **Podman execution.** `Image` in `ci.yml` with validation, the clone/run
   split, container invocation, startup check, failure modes.
3. **Deployment.** `deploy/` changes, subuid/subgid, the systemd override,
   pruning, and the operational notes.

## Tests

- A step cannot read the runner's SSH key.
- A step's environment contains what was constructed and nothing from the
  runner's own environment.
- Two concurrent builds cannot see each other's workspaces.
- An `image:` containing a shell metacharacter is refused by `ci.Parse`.
- A missing podman fails the runner at startup rather than running a build
  unsandboxed — the fallback that must not exist.
- A pull failure fails the build with the error in the log.

The isolation tests need podman on the machine running them. Gate them so the
suite still passes without it, and make the skip visible: a silently skipped
isolation test is how this regresses.

## What this does not do

No `network: none`, no per-step images, no image caching strategy beyond
pruning, no resource limits per build beyond what the service already applies.
Each is a separate decision.
