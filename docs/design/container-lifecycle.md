# Container lifecycle

Status: Phase 0 design, binding for Phase 1–4 implementation.
Related: [architecture/overview.md](../architecture/overview.md) §5 (control-plane
Task/Assignment lifecycles, which this document is downstream of),
[runtime.md](runtime.md), [failure-model.md](failure-model.md).

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
| `CREATED` | All setup steps succeeded; init process is cloned/forked but has not yet `execve`d the workload entrypoint (see `runtime.md` §3 for the exact synchronization point). | state file updated, PID + start-time recorded |
| `RUNNING` | Init process has called `execve`. This is the state whose invariants are enforced (§2). | state file updated |
| `STOPPING` | A stop was requested (signal sent to init, or restart-policy-triggered replacement). Waiting for exit or timeout. | state file updated |
| `EXITED` | Init process has exited. Exit code recorded. Resources (netns, cgroup, mounts) are *not yet* torn down — that's `DELETING`. | exit code + timestamp recorded |
| `DELETING` | Teardown of cgroup, network, and mounts in progress. | state file marked, deletion is idempotent (§4) |
| `FAILED` | Setup could not complete (image missing, mount failed, cgroup creation failed, etc.) before the workload ever ran. Distinct from `EXITED` because no user process ever executed. | error recorded |

`FAILED` and `EXITED` are both terminal-but-not-gone; both are followed by
`DELETING` when the reconciler decides the container is no longer wanted
(new generation, task terminated, or a restart policy consuming an `EXITED`
by creating a fresh container under a new `Attempt`).

## 2. Invariant for RUNNING

A container recorded as `RUNNING` implies, at the moment the invariant is
checked:

1. A state record for this exact `ContainerID` exists on disk.
2. A cgroup exists at the container's cgroup path.
3. A network namespace exists and is attached to the container's veth
   endpoint (once Phase 9 networking lands; before that, host netns).
4. The recorded PID identifies a live process **and** that process's
   `/proc/<pid>/stat` start-time matches the start-time recorded at
   `CREATED` (defends against PID reuse — see §5).
5. The recorded PID's root filesystem (via `/proc/<pid>/root`) resolves to
   this container's merged OverlayFS mount, not the host's or another
   container's.

Integration tests in Phase 1–4 assert this directly rather than trusting the
state file alone — the state file records *intent*, `/proc` is checked for
*fact*. A mismatch (state says RUNNING, `/proc` disagrees) is not silently
"fixed" by rewriting the state file to match; it is surfaced as a detected
inconsistency (`failure-model.md` §3) and drives reconciliation into
`FAILED`/`DELETING` cleanup, never a guess that the old process is still ours.

## 3. Transition ownership and ordering

| Transition | Owner | Ordering requirement |
|---|---|---|
| `ABSENT -> CREATING` | reconciler, on seeing a desired assignment with no local record | state file (recording target spec: image, resources, `ContainerID`) is written and **fsynced** before any namespace/cgroup/mount resource is created |
| `CREATING -> CREATED` | runtime, after namespaces + mounts + cgroup + init clone succeed, before execve | see `runtime.md` for the exact parent/child barrier |
| `CREATED -> RUNNING` | runtime, at execve | — |
| `CREATED/RUNNING -> FAILED` | runtime or reconciler, on any setup/launch error | partial resources from the failed attempt must be cleaned up before the state is considered settled (§4) |
| `RUNNING -> STOPPING` | reconciler, on stop request (generation superseded, scale-down, delete, restart) | — |
| `STOPPING -> EXITED` | runtime, on observed process exit (via reaper, see `runtime.md` PID 1 handling) or timeout-forced `SIGKILL` | — |
| `EXITED/FAILED -> DELETING` | reconciler | — |
| `DELETING -> ABSENT` | reconciler, after cgroup/mount/network teardown confirmed | state file removed **last**, after every host resource it referenced is gone |

The rule that recurs throughout: **the record of intent is durable before
the resource exists; the record of absence is durable only after the
resource is gone.** This is what makes restart-time recovery (§5) a matter
of reading disk state and checking `/proc`/sysfs, not guessing.

## 4. Idempotent retry / crash recovery

`gliderd` may be killed at any point. On restart it scans its local state
directory and, for every recorded container, re-evaluates:

- **State says `CREATING`, no further evidence**: setup never completed or
  crashed mid-way. Treat as `FAILED`, clean up whatever partial resources
  exist (cgroup dir if present, mounts if mounted — all torn down via the
  same idempotent teardown used for `DELETING`, which tolerates
  "already gone"), then let the reconciler decide whether to retry
  (`CREATING` again, new `Attempt`) based on current desired state.
- **State says `CREATED` or `RUNNING`**: check `/proc/<pid>` against the
  recorded start-time (§5). If it matches and looks healthy, resume
  observing it — `gliderd` does **not** recreate a container it can prove is
  still correctly running. If it doesn't match (PID gone, or reused by an
  unrelated process), treat as `EXITED` with an inferred/unknown exit code
  and proceed through normal cleanup.
- **State says `STOPPING`**: re-send the stop signal / re-arm the timeout;
  stopping is itself idempotent.
- **State says `DELETING`**: re-run teardown; every teardown step
  (`cgroup remove`, `unmount`, `netns delete`) is written to tolerate
  "resource does not exist" as success, not error.

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
not reuse for a different process at the same PID) as the identity tuple.
Any check of "is my recorded process still this process" compares both.
This is the same technique `runc`/`systemd` rely on and is deliberately not
reinvented as something more exotic — pidfds (`CLONE_PID` /
`pidfd_open`) are a stronger primitive worth adopting once the minimum
supported kernel is confirmed to make them universally available; noted as
a candidate improvement, not required for Phase 1–4 correctness.

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
