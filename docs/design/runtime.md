# Runtime design

Status: Phase 1 and Phase 2 implemented against this contract; see §7 for
what Phase 1 concretized and §8 for what Phase 2 added/superseded. §5 is
superseded by §8.2 — the container's init process (`glider-init`) remains
PID 1 for the container's whole life and supervises the workload as a
child, per [ADR-0006](../adr/0006-glider-init-pid1-supervisor.md); §5 is
kept below only as a historical record of the question Phase 1 left open.
Related: [container-lifecycle.md](container-lifecycle.md),
[failure-model.md](failure-model.md), [security-model.md](security-model.md).

This document specifies how Glider executes a container process on a single
Linux host: process architecture, namespace setup, synchronization, and
PID 1 behavior. It does not cover cgroups (Phase 4), image/OverlayFS
(Phase 5–7), capabilities/seccomp (Phase 8), or networking (Phase 9) beyond
noting where each will attach — those get their own docs when their phase
begins.

## 1. Process architecture

Glider uses a re-exec model, the same shape used by runc: a short-lived
"launcher" process configures namespaces and hands off to a second
invocation of the same binary running as the container's init.

```text
gliderd (or, standalone: glider-runtime run)
    │
    │ clone(CLONE_NEWPID|CLONE_NEWNS|CLONE_NEWUTS|CLONE_NEWIPC|CLONE_NEWNET|CLONE_NEWCGROUP)
    ▼
launcher process (parent, stays in caller's original namespaces except
                  where clone flags placed it in new ones)
    │
    │ configures the child's environment via the sync protocol (§3),
    │ then the child execs itself as `<binary> __glider_init__`
    ▼
container init (PID 1 inside the new PID namespace)
    │
    │ mount setup, pivot_root (§4)
    │
    │ execve(entrypoint)
    ▼
user workload process (still PID 1)
```

Rationale for re-exec over a single-process `clone()`-then-continue: Go's
runtime is multi-threaded before `main()` even starts (GC, sysmon, etc.),
and `CLONE_NEWPID` only takes effect for processes forked *after* the clone
— it does not retroactively move existing threads into a new PID namespace,
and a multi-threaded process cannot safely become PID 1 by simply calling
`unshare()` post-hoc. Re-exec guarantees the process that becomes PID 1 is a
fresh process image, started clean inside the new namespaces, matching how
runc/`libcontainer` solve the same constraint.

## 2. Namespaces in scope for Phase 1

| Namespace | Included in Phase 1 | Notes |
|---|---|---|
| PID | yes | container's init becomes PID 1 inside it |
| mount | yes | required for `pivot_root` (§4) |
| UTS | yes | isolated hostname |
| IPC | yes | isolated SysV IPC / POSIX MQ |
| network | yes, but **not configured** in Phase 1 | namespace is created (loopback-only); veth/bridge attachment is Phase 9 |
| cgroup | yes (namespace only) | actual cgroup v2 resource control is Phase 4; Phase 1 only isolates the cgroup namespace view |
| user | **not included in Phase 1** | deferred per master plan §9; revisited once core lifecycle is stable, since UID mapping interacts with filesystem ownership (Phase 6/7) in ways worth designing separately |

## 3. Parent/child synchronization

Sleep-based synchronization is disallowed (master plan §9, §37). The
launcher and the container init must agree, via an explicit signal, on when
each side is allowed to proceed, because several steps have hard ordering
requirements:

- The launcher must not report `CREATED` (container-lifecycle.md §3) until
  the init process has actually completed namespace setup and mounts — not
  merely been cloned.
- The init process must not `execve` the workload until the launcher has
  finished anything it needs to do from outside the new namespaces (e.g.,
  in later phases, writing `/proc/<pid>/uid_map` for user namespaces must
  happen from the parent, before the child proceeds past that point).

Mechanism: an `os.Pipe()`-based barrier per direction (child-ready pipe,
parent-ready pipe), read with a blocking `read()` of a single byte —
standard for this pattern, avoids polling or fixed sleeps entirely, and
lets errors on either side (closed pipe, `read` error) fail fast as a
distinguishable error rather than a hang. A context-scoped timeout wraps
the whole launch so a wedged child cannot hang `gliderd` indefinitely, but
the timeout is a backstop for detecting failure, never the mechanism used
to *sequence* correct-path execution.

```text
launcher                              init (child)
   │  clone()                              │
   │─────────────────────────────────────► │ (new namespaces active)
   │                                        │ configure mounts, hostname
   │                                        │ write "ready" to parent-pipe
   │  ◄─────────────────────────────────────│
   │  read parent-pipe -> proceed           │
   │  (do any parent-side-only setup)       │
   │  write "go" to child-pipe   ──────────►│  read child-pipe -> proceed
   │  record state = CREATED                │  pivot_root, execve
```

## 4. Mount setup and pivot_root

Sequence inside the container init, before `execve`:

1. `mount(MS_REC|MS_PRIVATE, "/")` in the new mount namespace — breaks
   propagation to/from the host **before** any other mount is created, so
   nothing done here can leak back to the host's mount table regardless of
   what propagation mode the host mounts used.
2. Bind-mount the prepared root filesystem (the OverlayFS `merged` dir, once
   Phase 7 exists; a plain directory for Phase 1) onto itself, so it can
   serve as the new root for `pivot_root` (which requires its target to be
   a mount point, not just a directory).
3. Create a directory for the old root inside the new root, `pivot_root`
   into it, then `chdir("/")`, `unmount(old_root, MNT_DETACH)`, and remove
   the now-empty old-root directory. `chroot` alone is explicitly rejected
   as insufficient (master plan §11) — it does not change the mount
   namespace's root, so a process could still reach the host filesystem
   through `..` traversal or a retained fd; `pivot_root` actually replaces
   the namespace's root mount.
4. Mount the expected in-container filesystems: `/proc` (fresh `procfs` —
   critical: this must be mounted *after* entering the new PID namespace,
   or it will show host processes), `/sys` (read-only in Phase 1; real
   restriction policy is part of `security-model.md`), `/dev` (a minimal
   tmpfs with standard nodes, not a bind-mount of the host's `/dev`),
   `/dev/pts`, `/dev/shm`.

Cleanup ordering and partial-failure tolerance for this sequence is governed
by container-lifecycle.md §4/§6 — a launch that fails partway through this
list produces a `FAILED` container whose teardown unwinds whatever subset of
these mounts exist.

## 5. PID 1 behavior (Phase 1 design; superseded by §8.2)

> This section is kept as a historical record of the question Phase 1
> deliberately left open (last paragraph below). §8.2 records the answer
> and the actual Phase 2 design; treat this section as background, not the
> current contract.

The container's init process is real PID 1 inside its namespace and
inherits the kernel's special PID 1 signal semantics: signals without an
explicit handler installed are **not** delivered using their default
disposition (e.g. an unhandled `SIGTERM` does not terminate PID 1 the way it
would any other process). Glider's init must therefore, before `execve`:

- Install explicit handlers for the signals used to stop containers
  (`SIGTERM`, `SIGINT`) that forward the signal to the workload process
  group, so `STOPPING` (container-lifecycle.md) actually reaches the
  workload rather than being silently absorbed by default PID 1 behavior.
- Reap any process whose parent has exited and been re-parented to PID 1
  (`wait4` in a loop on `SIGCHLD`), so orphaned children of the workload
  don't accumulate as zombies with nowhere else to go.
- Propagate the workload's exit code as its own exit code (container-
  lifecycle.md's `EXITED` state records this), since the control plane and
  operator-facing tooling need the real exit status, not init's.

Where the workload itself should be PID 1 (the common case, matching
`docker run` semantics for simple images) vs. Glider's own init binary
remaining PID 1 and exec'ing/forking the workload as a child is an open
question resolved during Phase 2 implementation, not frozen here — it
depends on how signal-forwarding and reaping are best composed, which is
easier to judge with a working prototype than in the abstract. Phase 1 only
needs to prove namespace + pivot_root + synchronized execve; PID 1 signal
handling is exercised properly starting Phase 2 (master plan phase list).

## 6. Phase 1 exit contract

**Command surface:**

```bash
sudo glider-runtime run --rootfs <path> -- <cmd> [args...]
```

**In scope:**

- Namespaces: PID, mount, UTS, IPC, network (created, unconfigured),
  cgroup (namespace view only) — table in §2.
- Re-exec launcher/init split (§1) with pipe-based synchronization (§3).
- `pivot_root`-based root filesystem transition (§4) against a plain
  pre-existing directory passed via `--rootfs` (no image pulling, no
  OverlayFS — that's Phase 5–7).
- Mounting `/proc`, `/sys` (read-only), `/dev` (minimal tmpfs), `/dev/pts`,
  `/dev/shm` inside the new root.
- The container-lifecycle.md state machine through `CREATING -> CREATED ->
  RUNNING -> EXITED`, persisted to a local state file, for a single
  foreground-run container (no daemon, no reconciliation loop yet — that's
  `gliderd`, Phase 10).
- Deterministic, non-sleep-based parent/child synchronization (§3).

**Explicitly out of scope for Phase 1** (each is a later phase, listed so
"done" has a hard boundary):

- cgroup v2 resource limits (Phase 4) — cgroup *namespace* exists, nothing
  is written to `cgroup.max`/`memory.max`/etc.
- Any image handling, content store, or OverlayFS (Phase 5–7) — `--rootfs`
  is just a directory the caller prepared.
- Capabilities, `no_new_privs`, seccomp (Phase 8) — Phase 1 containers run
  with whatever privilege the invoking process had; this is explicitly not
  a security boundary yet, and must not be described as one.
- Network configuration (Phase 9) — namespace exists with loopback only, no
  veth/bridge/IP.
- User namespaces (deferred, §2).
- Any multi-container or daemon behavior — `gliderd` and its reconciliation
  loop start at Phase 10.

**Test environment:** Linux only, cgroup v2 host (the presence of a unified
`/sys/fs/cgroup` is asserted even though Phase 1 does not write to it, to
fail fast on unsupported hosts rather than produce a confusing later
error), run as root or with equivalent `CAP_SYS_ADMIN`-class privilege
required for namespace creation and `pivot_root`. macOS cannot run these
tests; they are gated behind a Linux build/test tag and executed in CI (or
locally) on a Linux VM.

**Expected failure handling:** every setup step in §3/§4 that can fail
(clone, mount, pivot_root, execve) returns a distinguishable wrapped error
surfaced to the caller — Phase 1 has no reconciler to retry on its behalf,
so "fail loudly with enough context to know which step failed" is the bar,
not "recover automatically."

**Exit gate:** integration tests (Linux-tagged) that assert, for a running
Phase 1 container: (a) it has its own PID namespace (its init is PID 1
inside, some other PID outside), (b) `/proc` inside shows only its own
process tree, (c) its root filesystem is not the host's (a file written
inside is invisible outside and vice versa), (d) sending `SIGTERM` from
outside reaches and terminates the workload, (e) the workload's exit code
is correctly propagated, (f) a second `run` against the same `--rootfs`
does not corrupt or interfere with a concurrently running first instance,
and (g) killing the launcher process after `CREATED` but before the test
sends any signal still leaves the container's state recoverable per
container-lifecycle.md §4 (exercised by a real `gliderd`-less recovery
check once that logic exists — noted here as the contract, implemented as
its own test in Phase 2 when the persistence layer for it is more than a
stub).

## 7. Phase 1 implementation notes

Concrete choices the implementation had to make within this contract —
clarifications of the design above, not new frozen decisions, so none of
these are ADRs:

- **Mount ordering.** §3's sync diagram and §4's numbered steps read as
  slightly different orderings in isolation. The implementation resolves
  them as: everything in §4 steps 1-2 and 4 (private remount, bind-mount
  root onto itself, and mounting `/proc`/`/sys`/`/dev`/`/dev/pts`/`/dev/shm`
  *under* the not-yet-current root, e.g. `<rootfs>/proc`) happens before
  the child signals "ready"; `pivot_root` itself (step 3) is the single
  action taken after receiving "go", immediately before `execve`. This
  matches §3's diagram exactly and keeps the actual root swap — the
  point-of-no-return step — minimal and last. Mounting at `<rootfs>/proc`
  before pivot_root and mounting at `/proc` after are mechanically
  identical (pivot_root only changes what "/" refers to, not where these
  submounts live), so this doesn't change what's mounted, only when.
- **Unified error channel.** A single pipe (CLOEXEC on the child's end)
  carries *both* explicit setup-failure messages (mount, pivot_root) and
  exec-failure detection (via the standard trick of the fd auto-closing on
  a successful `execve`, giving EOF-means-success). §3 only specified the
  ready/go barrier; this channel is the mechanism behind "Expected failure
  handling" above.
- **Container init and workload are the same OS process in Phase 1** — the
  re-exec'd init directly `execve`s the workload, becoming it, as §1's
  diagram shows. §5's open question (whether a later phase should instead
  keep Glider's own init as PID 1 and fork the workload as a child, for
  full signal-forwarding/reaping semantics) is unaffected by this — Phase 1
  implements the diagram's minimal case, Phase 2 decides whether to add a
  wrapper.
- **Exit-gate (d) test methodology.** Because an unhandled `SIGTERM` sent
  to a namespace's PID 1 is not delivered with default disposition (§5),
  the integration test's workload fixture explicitly traps `SIGTERM`
  rather than relying on default-terminate behavior no arbitrary Phase 1
  workload actually has. The test proves the launcher correctly delivers
  the signal to the right process across the namespace boundary, which is
  what Phase 1 can honestly guarantee; it does not claim every unmodified
  workload stops on `SIGTERM` without its own handler.
- **`/dev` population.** Individual device nodes (`null`, `zero`, `full`,
  `random`, `urandom`, `tty`) are bind-mounted from the host into the
  fresh per-container `tmpfs`, rather than created with `mknod` — avoids a
  `CAP_MKNOD` dependency for a Phase 1 scope that already assumes full
  privilege (§6 "privilege expectations").

## 8. Phase 2: container lifecycle, PID 1 supervision, and crash recovery

Phase 2's full architecture decision is [ADR-0006](../adr/0006-glider-init-pid1-supervisor.md);
this section is the operational contract that follows from it.

### 8.1 Process identity, end to end

```text
host launcher (glider-runtime run)     host-visible PID = the OS pid of
                                        this process, as usual
   │  clone(CLONE_NEWPID|...)
   ▼
glider-init (__glider_init__)          host-visible PID = observed by the
  namespace PID 1                      launcher directly via the clone's
                                        return value; namespace-visible
                                        PID = 1 (it's the first process in
                                        the new namespace)
   │  fork+exec (ordinary child, no new namespace)
   ▼
workload                               host-visible PID = not directly
  namespace PID 2+ (kernel-assigned,   observable by glider-init (a
  not asserted to be exactly 2 — see  process cannot learn another
  the note in the integration test    process's host-visible PID from
  suite: glider-init's own Go         inside a nested PID namespace by
  runtime threads consume PID/TID     any syscall — deliberate, not a
  numbers before the fork)            missing API); the launcher
                                       resolves it best-effort, after the
                                       fact, by scanning the host's own
                                       /proc for the process whose PPid
                                       is glider-init's host PID
                                       (identity.go's resolveChildIdentity)
```

Only `InitPID`/`InitStartTime` are load-bearing for recovery correctness
(§8.6). `WorkloadPID`/`WorkloadStartTime` are recorded on a best-effort,
observability-only basis.

### 8.2 PID 1 supervision (supersedes §5)

`glider-init` remains PID 1 for the container's entire life. It does not
`execve` into the workload — Phase 1's model — it `fork`s+`exec`s the
workload as an ordinary child (PID 2+), using Go's `os/exec`, which is
safe to call from an already-multi-threaded process (unlike Phase 1's
re-exec requirement for *becoming* a new namespace's PID 1 in the first
place — forking an ordinary child needs no fresh process image). No shell
wrapper is used.

`glider-init` owns, for the container's whole life:

- forking/exec'ing the workload and confirming its `execve` succeeded
  (§8.3);
- installing signal handlers and forwarding the documented signal policy
  (§8.4);
- reaping every process reparented to it, not just the workload (§8.5);
- the graceful-shutdown grace-period/SIGKILL escalation (§8.7);
- durably recording the workload's `EXITED` outcome itself (§8.8) —
  independent of whether its launcher parent is still alive.

### 8.3 Exec confirmation without re-exec

Phase 1's "unified error channel" (§7) relied on the *init process's own*
successful `execve` auto-closing a `CLOEXEC` pipe to signal success
implicitly. That trick doesn't apply to `glider-init` itself anymore,
since it never execs into the workload. Instead, `glider-init` calls
`exec.Cmd.Start()` for the workload: Go's `os/exec` already implements the
identical fork+`CLOEXEC`-error-pipe pattern internally for the child it
starts, and `Start()` blocks until that determination is made — a `nil`
error is reliable, synchronous confirmation that the workload's `execve`
succeeded.

The launcher↔`glider-init` result channel (fd 5, successor to Phase 1's
error pipe, same fd position) carries exactly one message before closing:

```text
"FAIL <reason>\n"   — setup or workload-launch failed; FAILED, no workload
                       ever ran
"RUNNING\n"         — the workload is running; no payload needed (§8.1)
```

A bare EOF with no recognizable message (glider-init crashed before
saying anything) is treated as failure, not success — deliberately, since
Phase 1's implicit "EOF means success via CLOEXEC" convention no longer
holds once `glider-init` doesn't exec itself (protocol.go).

### 8.4 Signal policy

`glider-init` installs handlers only for a fixed, documented set —
"forward every possible signal" was explicitly rejected:

| Signal | Behavior |
|---|---|
| `SIGTERM`, `SIGINT` | **Lifecycle signals.** Forwarded once, as the exact signal received (not normalized), to the workload's process group; starts the grace-period timer (§8.7). |
| `SIGHUP`, `SIGQUIT`, `SIGWINCH` | **Transparent forward.** Relayed to the workload's process group with no shutdown implication — common terminal/reload signals a supervised child expects to see like any normal shell child. |
| `SIGCHLD` | Consumed by the reaper (§8.5); never forwarded. |
| Everything else (`SIGKILL`, `SIGSTOP`, ...) | Not handled — either uncatchable by any process, or deliberately out of scope. |

### 8.5 Reaping and process groups

The workload is started in its own process group (`Setpgid: true`, leader
= the workload's own PID), which `glider-init` creates but is not itself a
member of. This gives a single deterministic signal-delivery target
(`kill(-pgid, sig)`) for the workload's whole tree. Limitation: a
descendant that calls `setsid()` leaves the group and stops receiving
forwarded signals — this is a lifecycle-delivery gap, not a security
property (security-model.md's namespace/capability/seccomp layers are the
actual isolation boundary, unaffected by this).

`glider-init` reaps via a `SIGCHLD`-driven, `wait4(-1, WNOHANG)` loop
(reap.go) — event-driven, no polling — installed *before* the workload is
started, closing the race where a signal handler installed only after
`Start()` could miss a workload that exits immediately. Every sweep drains
*all* currently-exited children, not just the main one, so a burst of
simultaneous exits (main workload plus stragglers) doesn't leave zombies
just because the main one was observed first.

**Policy for descendants surviving past the main workload's exit:**
`glider-init` does not wait for them. Once the main workload has exited,
it sends `SIGKILL` to the workload's process group (best-effort, catches
everything that didn't `setsid()` away) and performs one bounded,
non-blocking drain sweep, then proceeds directly to recording `EXITED` and
exiting itself. Any process still alive in the namespace at that point —
including a `setsid()`'d escapee the process-group signal never reached —
is unconditionally force-killed by the kernel the instant `glider-init`
(the namespace's PID 1) exits: a PID namespace's init exiting tears down
every remaining process in it, immediately, with no exceptions. This is
the actual safety mechanism; the process-group `SIGKILL` above is a
courtesy for prompt cleanup, not what prevents leaks.

### 8.6 Recovery: kernel-guaranteed liveness equivalence

Because `glider-init` is PID 1 of its own PID namespace, **its liveness
(validated by recorded PID + start-time, container-lifecycle.md §5) is
equivalent to the whole container's liveness** — if it's gone, the
workload and every descendant are unconditionally gone too (§8.5's kernel
guarantee). Recovery therefore only ever checks one process identity, not
a process tree, and never fabricates a guessed exit code — see ADR-0006's
"Recovery policy" section and container-lifecycle.md §4/§5 for the full
recovery matrix.

A live `glider-init` does not depend on its launcher parent to keep
functioning; if the launcher dies, `glider-init` is simply reparented to
the host's real init and continues supervising unaffected. Recovery
therefore never attempts to "adopt" a live container — there is nothing to
adopt.

### 8.7 Graceful shutdown protocol

```text
external SIGTERM/SIGINT reaches glider-runtime (the launcher)
        ↓
launcher: STOPPING written durably, forwards the SAME signal to
          glider-init's host PID (not normalized to SIGTERM — the actual
          signal is preserved end to end)
        ↓
glider-init: forwards the same signal to the workload's process group,
             starts its own monotonic grace-period timer (config.go's
             StopGrace; default 10s, overridable via --stop-grace or
             Config.StopGrace — production default unchanged from Phase 1,
             now configurable so tests don't have to wait 10s for real)
        ↓
workload exits within the grace period?
   ├── yes → reap → glider-init durably records EXITED (§8.8) → exits
   └── no  → glider-init sends SIGKILL to the workload's process group →
             reap → records EXITED → exits
```

The launcher keeps its own **outer backstop** deadline
(`StopGrace + outerBackstopBuffer`, strictly longer than glider-init's own
— config.go) in case `glider-init` itself hangs; under normal operation
glider-init's internal escalation always finishes first, so the backstop
essentially never fires. If it does, the launcher SIGKILLs `glider-init`
directly, which safely tears down the whole namespace per §8.5's kernel
guarantee.

### 8.8 State ownership split and the result-channel/state-file boundary

| Transition | Owner | Notes |
|---|---|---|
| `ABSENT -> CREATING` | launcher | unchanged from Phase 1 |
| `CREATING -> CREATED` | launcher | records `InitPID`/`InitStartTime` — the one moment the launcher can observe glider-init's host PID directly, via the `clone()`/`Start()` return value |
| `CREATED -> RUNNING` | launcher | triggered by glider-init's `RUNNING` result-channel message (§8.3); best-effort resolves `WorkloadPID` (§8.1) |
| `RUNNING -> STOPPING` | launcher | on an external lifecycle signal (§8.7) |
| `RUNNING`/`STOPPING -> EXITED` | **glider-init itself** | new in Phase 2 — see below |
| `EXITED`/`FAILED -> DELETING -> ` (removed) | recovery (`process.Recover`) | Phase 2 has no daemon to trigger this automatically; see container-lifecycle.md §4 |

`glider-init` writes `EXITED` directly (init.go's `persistExited`) because
it has the only authoritative knowledge of the real exit code and does not
depend on the launcher still being alive to relay it — this is what makes
"launcher dies while RUNNING" recoverable without inventing a re-adoption
protocol (§8.6).

Two mechanics make this safe:

- **Filesystem reachability across `pivot_root`.** `glider-init` opens a
  directory fd on the container's state directory *before* calling
  `pivot_root` and keeps it open (marked `CLOEXEC` so the workload never
  inherits it — Phase 2 §18's descriptor-ownership discipline). After
  `pivot_root`, the host-side state directory is no longer reachable by
  path at all (that's the whole point of `pivot_root`); the pre-opened fd
  remains valid via the `openat(2)` family regardless
  (`state.SaveAt`/`LoadAt`/`LockAt`, state/fdops_linux.go).
- **Ordering race between the launcher's RUNNING write and glider-init's
  EXITED write.** These are two independent processes racing on the same
  state file with no direct synchronization between "glider-init confirmed
  RUNNING over the result channel" and "the launcher's disk write of
  RUNNING actually landed" — for an extremely short-lived workload,
  glider-init can observe the exit before that write lands. `persistExited`
  handles this with a bounded wait/retry (outside the per-container lock,
  to avoid deadlocking against the launcher's own attempt to acquire it)
  for the state to reach `RUNNING`/`STOPPING` before it will write
  `EXITED` — not "sleep and hope" test-style synchronization, but a
  bounded wait for a genuinely fast, expected write by a cooperating
  process (init.go's `exitedWaitForRunningTimeout`).

If `glider-init` exits without ever completing this write (killed
mid-flight, OOM, a genuine crash), the launcher — if it's still alive to
observe `glider-init`'s own process exit — applies the identical
identity-check-based convergence logic Recover uses
(`convergeAfterUnexpectedInitExit`, launcher.go), marking the record
`EXITED` with `ExitedInferred: true` and surfacing a distinguishable
`SupervisorFailureError` (mapped to `cmd/glider-runtime`'s dedicated exit
code 125 — §8.9) rather than silently reporting exit code 0.

### 8.9 Exit code semantics

| Case | `glider-runtime`'s own process exit code |
|---|---|
| Workload exits normally with code N | N |
| Workload terminated by signal S | 128+S |
| Setup/launch failure before RUNNING (FAILED, no workload ever ran) | 1, with a clear stderr message |
| Supervisor failure after RUNNING (glider-init crashed/killed before recording EXITED) | 125 (`SupervisorFailureError`, matching the Docker CLI's convention for "the runtime itself errored") |

### 8.10 Locking

A per-container advisory lock (`flock(2)` on `<state-dir>/<id>/lock`,
state/lock.go) serializes concurrent lifecycle-mutating operations — the
launcher and glider-init both writing the same state file at different
points in the container's life, or two concurrent `Recover` calls. `flock`
was chosen specifically for its crash behavior: the kernel releases the
lock automatically when the holding process's fd closes, including on a
crash, so there is never a stale lock to detect or clean up by hand — this
is node-local concurrency control, not distributed consensus. The
container's directory is created exactly once, by the `CREATING`
transition — `TryLock` deliberately never creates it on demand (a subtlety
that mattered in practice: an earlier version resurrected an empty
directory for a container `Recover` had already fully deleted, simply by
someone calling `Recover` again on the same, now-nonexistent, ID).

### 8.11 pidfd: evaluated, not adopted

Linux `pidfd_open`/`pidfd_send_signal` were evaluated as a stronger
PID-reuse-safe primitive for live process supervision. Not adopted in
Phase 2:

- The only place Phase 2 needs to *signal* a process by recorded identity
  across a process boundary is recovery signaling a stale/dead
  `glider-init` — but recovery's actual policy (§8.6) never signals a
  live, healthy `glider-init` at all (there's nothing to adopt), so a
  pidfd's main advantage (race-free signal delivery to a process you
  didn't just create) has no live use case yet in this phase's scope.
- pidfds cannot be persisted across a process restart (they're a live
  kernel object, not serializable state) — the PID + start-time tuple
  (container-lifecycle.md §5) is what's actually usable for durable,
  crash-surviving identity, and pidfds don't replace it, only potentially
  complement it for a live handle.
- Reaping (§8.5) is entirely local to `glider-init` itself, via ordinary
  `wait4`/`SIGCHLD` — the process doing the reaping is the direct parent,
  which has no PID-reuse ambiguity to begin with (a parent's `wait4` on
  its own children is inherently safe; the PID-reuse hazard is specifically
  about a *different* process acting on a *recorded* PID later).

Candidate for reconsideration once a real `gliderd` needs true live
re-attachment/adoption semantics (ADR-0006's "what would cause
reconsideration"), evaluated then against the actual minimum supported
kernel.
