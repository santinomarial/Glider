# Container lifecycle

Status: Phase 0 design, binding through Phase 8 implementation. Phase 2
(docs/adr/0006) froze who durably publishes each transition now that a
container's init (`glider-init`) and its workload are different processes
— see the "Owner" column in §3 and the note under §1's table; the state
*names* and their meanings are unchanged from this document's original
design. Phase 4 ([cgroups.md](cgroups.md)) implements §2 invariant 2's
"a cgroup exists" clause, which was frozen here from Phase 0 but not
actually enforced until now.
Related: [architecture/overview.md](../architecture/overview.md) §5 (control-plane
Task/Assignment lifecycles, which this document is downstream of),
[cgroups.md](cgroups.md) (Phase 4 cgroup v2 resource isolation),
[runtime.md](runtime.md) §8 (Phase 2 implementation), [failure-model.md](failure-model.md).

This document specifies the lifecycle of a single container as owned by
`gliderd` on one node. It is node-local, disk-backed state — distinct from
the control-plane Task/Assignment objects in `overview.md` §5.2–5.3. A
container corresponds to one `(TaskID, Generation, Attempt)` (§4 of
overview.md).

## 1. States

```text
ABSENT ──► CREATING ──► CREATED ──► RUNNING ──► STOPPING ──► EXITED ──► DELETING ──► ABSENT
              │             │           │                       ▲
              └─────────────┴───────────┴───────► FAILED ───────┘
                        (creation or runtime setup error)
```

| State | Meaning | On-disk record |
|---|---|---|
| `ABSENT` | No record exists. Either never created, or fully deleted. | none |
| `CREATING` | `gliderd` has committed to creating this container and is executing the setup sequence (rootfs, netns, cgroup, namespaces). Not yet running user code. | state file written *before* any host-visible resource is created (§3) |
| `CREATED` | All setup steps succeeded; the container's init process (`glider-init`, Phase 2 — runtime.md §8) exists and its host identity is durably recorded, but the workload has not yet been forked/exec'd (see `runtime.md` §3/§8.3 for the exact synchronization point). | state file updated, init's PID + start-time recorded (`InitPID`/`InitStartTime`) |
| `RUNNING` | The workload's `execve` has been confirmed (runtime.md §8.3). This is the state whose invariants are enforced (§2). | state file updated; workload's PID + start-time recorded best-effort (`WorkloadPID`/`WorkloadStartTime` — observability only, not load-bearing) |
| `STOPPING` | A stop was requested (signal forwarded to init, or restart-policy-triggered replacement). Waiting for exit or timeout. | state file updated |
| `EXITED` | The workload has terminated and its exit status has been recorded. `glider-init` performs final descendant cleanup and exits shortly after, but this state is keyed to the *workload's* termination, not `glider-init`'s own (runtime.md §8.8) — an exit code recovery could not directly observe (e.g. `glider-init` itself crashed first) is recorded with `ExitedInferred: true` rather than a fabricated number (§4, §5). Resources (netns, cgroup, mounts) are *not yet* torn down — that's `DELETING`. | exit code + timestamp recorded |
| `DELETING` | Teardown of cgroup, network, and mounts in progress. | state file marked, deletion is idempotent (§4) |
| `FAILED` | Setup could not complete (image missing, mount failed, cgroup creation failed, etc.) before the workload ever ran. Distinct from `EXITED` because no user process ever executed. | error recorded |

`FAILED` and `EXITED` are both terminal-but-not-gone; both are followed by
`DELETING` when the reconciler decides the container is no longer wanted
(new generation, task terminated, or a restart policy consuming an `EXITED`
by creating a fresh container under a new `Attempt`). Phase 2 has no
reconciler yet (`gliderd` is Phase 10) — `EXITED`/`FAILED -> DELETING ->`
(removed) is driven explicitly today, by `process.Recover`
(runtime.md §8.6, §8.8), which also handles resuming a `DELETING` crashed
mid-teardown.

## 2. Invariant for RUNNING

A container recorded as `RUNNING` implies, at the moment the invariant is
checked:

1. A state record for this exact `ContainerID` exists on disk.
2. A cgroup exists at the container's cgroup path (Phase 4,
   [cgroups.md](cgroups.md) §1/§3 — the path is deterministic from
   `ContainerID`, not read from the state record's `CgroupPath` field,
   consistent with this section's own rule that the state file records
   intent, `/proc`/cgroupfs is checked for fact).
3. A network namespace exists and is attached to the container's veth
   endpoint (once Phase 9 networking lands; before that, host netns).
4. The recorded **init** PID (`InitPID` — Phase 2, runtime.md §8.1; this is
   `glider-init`, not the workload) identifies a live process **and** that
   process's `/proc/<pid>/stat` start-time matches the start-time recorded
   at `CREATED` (defends against PID reuse — see §5). Because `glider-init`
   is PID 1 of the container's own PID namespace, the kernel guarantees
   this single check is equivalent to "the whole container (workload
   included) is alive" — runtime.md §8.6 — so this is the only process
   identity recovery ever needs to validate, not a tree walk.
5. The recorded init PID's root filesystem (via `/proc/<pid>/root`)
   resolves to this container's merged OverlayFS mount, not the host's or
   another container's. This is enforced before `RUNNING` publication for
   both caller-provided rootfs directories and Phase 7 OverlayFS snapshots.

Integration tests through Phase 8 assert this directly rather than trusting the
state file alone — the state file records *intent*, `/proc` is checked for
*fact*. A mismatch (state says RUNNING, `/proc` disagrees) is not silently
"fixed" by rewriting the state file to match; it is surfaced as a detected
inconsistency (`failure-model.md` §3) and drives reconciliation into
`FAILED`/`DELETING` cleanup, never a guess that the old process is still ours.

## 3. Transition ownership and ordering

| Transition | Owner | Ordering requirement |
|---|---|---|
| `ABSENT -> CREATING` | launcher (Phase 1-2; reconciler once `gliderd` exists) | state file (recording target spec: image, resources, `ContainerID`) is written and **fsynced** before any namespace/cgroup/mount resource is created; the container's on-disk directory is created here and only here (runtime.md §8.10) |
| `CREATING -> CREATED` | launcher, after namespaces + mounts + `glider-init` clone succeed, before the workload starts | see `runtime.md` §3/§8.3 for the exact parent/child barrier; records `InitPID`/`InitStartTime`, the one moment the launcher can observe `glider-init`'s host PID directly |
| `CREATED -> RUNNING` | launcher, on `glider-init`'s confirmed workload `execve` (runtime.md §8.3) | best-effort resolves `WorkloadPID` (runtime.md §8.1) |
| `CREATED/RUNNING -> FAILED` | launcher, on any setup/launch error | partial resources from the failed attempt must be cleaned up before the state is considered settled (§4); no user process ever ran, so this is distinct from `EXITED` |
| `RUNNING -> STOPPING` | launcher, on external stop request (SIGTERM/SIGINT to `glider-runtime`, forwarded — runtime.md §8.7) | — |
| `RUNNING`/`STOPPING -> EXITED` | **`glider-init` itself** (Phase 2, runtime.md §8.8 — not the launcher) | `glider-init` has the only authoritative knowledge of the workload's real exit code and does not depend on its launcher parent being alive to record it; this is what makes "launcher dies while RUNNING" recoverable without a re-adoption protocol (runtime.md §8.6) |
| `EXITED/FAILED -> DELETING` | `process.Recover` (Phase 2's explicit recovery entry point; reconciler once `gliderd` exists) | — |
| `DELETING -> ABSENT` | `process.Recover` / reconciler, after cgroup/mount/network teardown confirmed | state file removed **last**, after every host resource it referenced is gone |

The rule that recurs throughout: **the record of intent is durable before
the resource exists; the record of absence is durable only after the
resource is gone.** This is what makes restart-time recovery (§5) a matter
of reading disk state and checking `/proc`/sysfs, not guessing.

## 4. Idempotent retry / crash recovery

`gliderd` may be killed at any point. On restart it scans its local state
directory and, for every recorded container, re-evaluates — Phase 2
implements exactly this matrix today, as `process.Recover(stateRoot,
containerID)` (runtime.md §8.6), since no `gliderd` exists yet to run it
automatically (invoked explicitly: an operator via
`glider-runtime recover`, or a test):

- **State says `CREATING` or `CREATED`, init not alive**: setup never
  completed, or completed but the workload never started, before the
  owning process was lost. Treat as `FAILED` (`CREATING` never even had a
  recorded PID to check — it converges to `FAILED` unconditionally, per
  this rule's original wording; `CREATED` first validates the recorded
  `InitPID`/`InitStartTime`, §5). No host resources need explicit cleanup
  in the current architecture: `glider-init`'s mounts live in its own
  private, `MS_PRIVATE` mount namespace (runtime.md §4), which the kernel
  destroys automatically once the sole process that was ever in it exits —
  the only artifact of this crash is the stuck state record itself. This
  closes Phase 1's deferred exit-gate test (runtime.md §6 exit-gate (g)).
- **State says `CREATING` or `CREATED`, init still alive**: do nothing —
  a legitimate launch may still be in progress under its own, still-alive
  launcher; recovery does not race a genuine in-flight launch (Phase 2
  explicitly rejects inventing a "magical resume" for this case — there is
  nothing unsafe about simply observing and reporting, since nothing here
  needs fixing).
- **State says `RUNNING` or `STOPPING`**: check the recorded **init**
  identity (`InitPID`/`InitStartTime`, not the workload's — §5) against
  `/proc`. If it matches and is alive, the container is genuinely still
  running and self-supervising (runtime.md §8.2, §8.6) — nothing to do,
  and specifically **no adoption**: `glider-init` doesn't need its
  launcher alive to keep functioning correctly. If it doesn't match (PID
  gone, or reused by an unrelated process), the kernel's PID-namespace
  guarantee (runtime.md §8.6) means the workload and every descendant are
  unconditionally gone too — converge directly to `EXITED` with
  `ExitedInferred: true` (never a guessed exit code) and proceed through
  normal cleanup.
- **State says `STOPPING`**: covered by the `RUNNING`/`STOPPING` case
  above — stopping is itself idempotent under it.
- **State says `EXITED` or `FAILED`**: advance through `DELETING` to
  `ABSENT` (idempotent cleanup, this section's next bullet) — Phase 2 has
  no reconciler to defer this decision to yet, so `process.Recover`
  performs it directly rather than leaving a terminal record to accumulate
  forever.
- **State says `DELETING`**: re-run teardown; every teardown step
  (`cgroup remove`, `unmount`, `netns delete`) is written to tolerate
  "resource does not exist" as success, not error. The container's
  advisory lock (runtime.md §8.10) is held for `Recover`'s entire
  operation, so two concurrent recovery attempts against the same
  container serialize rather than race destructively — one converges the
  state, the next (if any) observes the converged result and proceeds
  idempotently from there, never re-deriving or guessing intermediate
  state.

No transition requires knowing "did I already do this?" from memory alone —
every step is defined so that redoing it when it already succeeded is a
no-op, which is what makes `Ensure*` semantics (overview.md §2) hold in
practice, not just in name.

## 5. PID reuse and process identity

Linux recycles PIDs. A state file that records only a PID is not a safe
identity across a `gliderd` restart or even across a slow reconciliation
pass, because the process behind that PID may have exited and the number
reassigned to something unrelated (including, in adversarial or unlucky
cases, another Glider container).

Glider records **PID + process start time** (`starttime` field from
`/proc/<pid>/stat`, which is a monotonic-since-boot value the kernel does
not reuse for a different process at the same PID) as the identity tuple —
implemented as `process.ProcessIdentity` /
`CaptureProcessIdentity`/`ValidateProcessIdentity`, centralized rather than
parsed ad hoc at each call site (Phase 2 §30; the parser is tested against
adversarial `comm` fields containing spaces and parentheses). As of Phase 2
this identity is recorded for **the container's init (`glider-init`), not
the workload** (`InitPID`/`InitStartTime` — runtime.md §8.1); validating it
also confirms the workload's liveness, transitively, via the kernel's
PID-namespace guarantee (runtime.md §8.6), so a separate check against the
workload's own PID is not needed for recovery correctness. Any check of
"is my recorded process still this process" compares both PID and
start-time, and additionally treats a zombie (state `Z`) as not-alive —
a zombie has already exited in every sense that matters here, even though
`/proc/<pid>` still resolves for it.

This is the same technique `runc`/`systemd` rely on and is deliberately not
reinvented as something more exotic — pidfds (`CLONE_PID` /
`pidfd_open`) were evaluated in Phase 2 (runtime.md §8.11) and not adopted:
Phase 2's recovery policy never needs to signal a *live* process by a
freshly-observed handle (a live `glider-init` is never adopted — runtime.md
§8.6), and pidfds cannot be persisted across the exact kind of process
restart this identity tuple exists to survive. Noted as a candidate
improvement for a future phase that needs true live re-attachment
(ADR-0006's "what would cause reconsideration"), not required for Phase
1–4 correctness.

## 6. Cleanup semantics

Cleanup (the `DELETING` path) must tolerate partial failure and partial
prior completion:

- Unmounting is attempted for every mount this container is recorded as
  having created, in reverse order of creation; a mount that is already gone
  is success, not an error.
- cgroup removal requires the cgroup to have no live processes; `DELETING`
  first ensures the init process (and, transitively, anything it failed to
  reap) is gone before removing the cgroup directory. A cgroup that won't
  remove because a process is still attached is a detected-not-silently-
  ignored failure (surfaced, retried) — see `failure-model.md`.
- Network resources (veth, IP allocation) are released idempotently; an IP
  release for an address not currently held by this container is a no-op,
  not an error, so double-release from an overlapping retry does not corrupt
  the allocator.
- The on-disk state file is removed only after every referenced resource is
  confirmed absent, so a crash mid-cleanup resumes cleanup on the same
  container on restart rather than losing track of it.
