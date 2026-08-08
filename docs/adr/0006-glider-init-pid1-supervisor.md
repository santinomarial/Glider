# ADR 0006: Glider init remains PID 1; workload runs as a supervised child

## Status

Accepted.

## Context

`runtime.md` §5 (Phase 1) left open whether the container's namespace init
process should directly `execve` into the user's workload (becoming it, and
thus becoming PID 1 itself), or whether a small Glider-owned init should
remain PID 1 and run the workload as a child it supervises. Phase 1
implemented the former only because it was the minimal path to proving
namespace + `pivot_root` + synchronized `execve` (`runtime.md` §7); it
explicitly deferred the PID 1 supervision question to Phase 2
(master plan phase list).

Phase 2's mandate is container lifecycle correctness: signal handling,
zombie/orphan reaping, deterministic shutdown, and crash recovery. All four
depend directly on this decision.

## Problem

A namespace's PID 1 has special kernel semantics: signals without an
explicit handler are not delivered with default disposition, and PID 1 is
responsible for reaping every process that gets reparented to it (any
orphaned descendant of the workload). If the *workload itself* is PID 1
(direct-exec, Phase 1's model):

- The workload must itself implement PID 1 signal handling and reaping
  correctly, or SIGTERM/SIGINT are silently absorbed and orphaned
  descendants accumulate as zombies with nowhere to be reaped. Almost no
  ordinary program is written to do this (this is exactly why wrapper inits
  like `tini`/`dumb-init` exist for Docker).
- Glider has no place to install its own lifecycle policy (grace-period
  escalation, process-group signaling, deterministic exit-code mapping)
  without reaching into the workload's own process.

## Decision

Glider's own init (`glider-init`, the re-exec'd `__glider_init__` entrypoint)
**remains PID 1 for the lifetime of the container** and runs the workload as
a supervised child (PID 2+ inside the namespace):

```text
host launcher (glider-runtime run)
   │  clone(CLONE_NEWPID|...)
   ▼
glider-init            host-visible PID = launcher's child PID
  (namespace PID 1)    namespace-visible PID = 1
   │  mount setup, pivot_root, fork+exec
   ▼
workload                host-visible PID = some host PID > glider-init's
  (namespace PID 2+)     namespace-visible PID = some namespace PID > 1
```

`glider-init` owns: forking/execing the workload, installing signal
handlers, forwarding signals, reaping all descendants (`wait4`-on-`SIGCHLD`,
not polling), grace-period shutdown escalation, and mapping the workload's
termination to `glider-init`'s own process exit status. No shell wrapper is
introduced — `glider-init` directly `fork`s+`exec`s the requested
executable via Go's `os/exec`, which is safe to call from an
already-running multi-threaded process (unlike Phase 1's re-exec
requirement for *becoming* PID 1 of a new namespace, forking an ordinary
child does not require a fresh process image).

### Exec confirmation without re-exec

Phase 1's "unified error channel" (`runtime.md` §7) relied on the *init
process's own* successful `execve` auto-closing a `CLOEXEC` error pipe to
signal "setup succeeded" implicitly. That trick no longer applies to
`glider-init` itself, since `glider-init` never execs into the workload —
it forks a child. Instead, `glider-init` uses `exec.Cmd.Start()` for the
workload: Go's `os/exec` already implements the identical fork+`CLOEXEC`
error-pipe pattern internally for the *child* it starts, and `Start()`
blocks until that determination is made — a nil error from `Start()` is a
reliable, synchronous confirmation that the workload's `execve` succeeded.
`glider-init` reuses this rather than re-inventing a second exec-confirmation
pipe.

The launcher↔`glider-init` result channel (successor to Phase 1's error
pipe) is extended to a small textual protocol (`FAIL`, `RUNNING <pid>
<starttime>`, `EXITED <code>`, `SUPERVISOR_ERROR`) so the launcher gets
explicit, typed outcomes instead of inferring meaning from a bare OS exit
status — see `runtime.md` §3/§7 (updated) for the wire format.

### State ownership split

`glider-init` durably writes the container's `EXITED` transition itself
(it already knows the workload's real exit code and the state directory),
rather than relying on the launcher to observe it. This is what makes
"launcher dies while RUNNING" recoverable without inventing a re-adoption
protocol (§ below) — the workload's fate is recorded by the process that
actually has the information, independent of whether the launcher is still
alive to relay it.

### Recovery policy: no re-adoption, kernel-guaranteed liveness equivalence

A live `glider-init` does not depend on its launcher parent to keep
functioning — once RUNNING, it supervises independently. If the launcher
dies, `glider-init` is simply reparented to the host's real init and
continues unaffected; an operator (or a future `gliderd`) can still signal
it directly using the PID + start-time durably recorded at `CREATED`.
Phase 2 recovery therefore does not attempt to "adopt" a live container for
continued signal-forwarding — there is nothing to adopt; `glider-init` is
already doing that job.

This also gives a clean, provable recovery invariant instead of a
heuristic one: because `glider-init` is PID 1 of its own PID namespace, the
kernel guarantees that if `glider-init`'s process ever exits or is killed,
every other process remaining in that namespace is immediately force-killed
and the namespace is torn down. **`glider-init`'s liveness (validated by
recorded PID + start-time) is therefore equivalent to the whole container's
liveness.** Recovery only ever needs to check one process identity, not
walk a process tree: if `glider-init` is gone, the workload and all its
descendants are unconditionally gone too, and recovery can safely converge
the state record without guessing at an unobserved exit code (recorded as
"inferred", never fabricated as a specific number).

## Alternatives considered

- **Direct-exec (Phase 1's model), permanently.** Rejected: pushes PID 1
  correctness onto every workload image, which is exactly the failure mode
  `tini`/`dumb-init` exist to paper over for other runtimes. Glider owning
  it is strictly more correct and is what the master plan's Phase 2 mandate
  (correct PID 1 supervision) requires.
- **A shell wrapper as PID 1** (e.g. exec a tiny shell script that traps
  signals and execs the workload). Rejected by the phase brief directly,
  and independently a bad fit: shells have their own inconsistent signal
  and job-control semantics across implementations, and reintroduce exactly
  the "does this wrapper correctly reap zombies" problem Glider is trying
  to own deliberately in Go instead.
- **Re-exec `glider-init` a second time to become the workload once its own
  supervision role is no longer needed** (i.e., only supervise until first
  exit, then exec-replace). Rejected: there is no point at which
  "supervision is no longer needed" while the workload might still spawn
  orphans that need reaping; the whole value of this ADR is continuous
  supervision for the container's entire lifetime.
- **Recovery re-adopts a live container by re-opening a fresh IPC channel
  to the orphaned `glider-init`.** Rejected: anonymous pipes cannot be
  reconstructed after the original holder's process is gone (this is
  explicitly the trap the Phase 2 brief warns against), and it is
  unnecessary — `glider-init` doesn't need an active launcher to keep
  supervising, so there is nothing to re-establish.

## Consequences

- `glider-init` is a long-lived process for the container's entire life,
  not a short-lived launcher step — its own crash/OOM-kill is now a
  meaningful failure mode, mitigated by the kernel liveness-equivalence
  property above (no zombie workload can outlive a dead `glider-init`).
- The launcher's role after `RUNNING` shrinks to: forward external
  lifecycle signals to `glider-init`'s host PID, wait for `glider-init`'s
  own process to exit, and read back the state `glider-init` already
  recorded — it no longer computes the container's exit code from a raw
  Unix wait status itself.
- Two independent writers (launcher and `glider-init`) can touch the same
  state file at different points in the container's life; per-container
  advisory locking (`flock` on a lock file in the container's state
  directory) is introduced to keep concurrent lifecycle operations
  (including a future recovery pass) from racing destructively.
- `container-lifecycle.md`'s `CREATED`/`RUNNING`/`EXITED` semantics are
  updated to reflect who durably publishes each transition now that init
  and workload are different processes (see that document, updated
  alongside this ADR).

## Risks

- Centralizing reaping/signal-forwarding logic in `glider-init` means a bug
  there affects every container, versus Phase 1 where a bug was scoped to
  one workload's own signal handling. Mitigated by the unit + privileged
  integration test suite added in Phase 2 (zombie/orphan stress, signal
  policy tests, race-detector runs) rather than by design alone.
- `glider-init` writing `EXITED` itself, concurrently with a possible
  in-flight recovery read, requires correct lock discipline; a bug here
  could produce a torn or racy transition. Mitigated by `state.Save`'s
  existing atomic rename-based publication plus the new per-container lock.

## What would cause reconsideration

If a future phase needs true live re-attachment (a `gliderd` process that
must resume active bidirectional supervision of a container after its own
restart, not just observe it), the "no re-adoption" policy here would need
revisiting — likely via `pidfd`-based live handles, evaluated in that
phase against the then-current minimum supported kernel (see the pidfd
discussion in `runtime.md` §3, not adopted in Phase 2 — see that section
for why the existing PID + start-time technique remains sufficient for
Phase 2's scope).
