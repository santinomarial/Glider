# Runtime design

Status: Phase 0 design; §5 is the binding Phase 1 exit contract.
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

## 5. PID 1 behavior

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
