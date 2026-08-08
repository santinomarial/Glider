# cgroup v2 resource isolation

Status: Phase 4 implemented against this contract.
Related: [runtime.md](runtime.md) §8 (Phase 2 PID 1 supervision, which this
builds directly on top of), [container-lifecycle.md](container-lifecycle.md),
[failure-model.md](failure-model.md),
[ADR-0001](../adr/0001-linux-cgroup-v2-only.md) (cgroup v2 only, frozen
Phase 0).

This document specifies how Glider gives each container a dedicated
cgroup v2 subtree with enforceable CPU, memory, and PID-count limits. It
does not cover I/O enforcement (deferred, §9), capabilities/seccomp
(Phase 8), or networking (Phase 9).

## Note on phase numbering

The original roadmap listed "Phase 3 — Mount isolation and pivot_root"
before "Phase 4 — cgroup v2 resource isolation". Phase 1's exit contract
(runtime.md §6) already required and implemented `pivot_root`-based root
filesystem isolation as part of proving namespace + synchronized `execve`
— that work does not need to be redone, and Phase 1's commit is not
renumbered or rewritten. Phase 3's intended scope was therefore completed
early, inside Phase 1; this document begins the next actual increment of
new work, matching the "Phase 4" label used throughout Glider's history
(commits, ADRs, prior design docs) rather than inventing a "Phase 3" that
never had its own separate implementation to point to.

## 1. cgroup v2 hierarchy and the Glider subtree

Glider discovers the host's cgroup v2 mount point by parsing
`/proc/self/mountinfo` rather than assuming `/sys/fs/cgroup`
(`internal/runtime/cgroup/discover.go`) — a host could mount it elsewhere,
and hard-coding the conventional path would silently do the wrong thing on
one that doesn't. ADR-0001's "cgroup v2 only" decision is enforced the
same way runtime.md's pre-Phase-4 check did: if no `cgroup2`-typed mount is
found, or it lacks a `cgroup.controllers` file, Glider refuses to start
(`cgroup.ErrUnsupported`) rather than degrading silently.

Every container gets a cgroup at a **fixed** location:

```text
<cgroup2-mount>/glider/<container-id>/
```

`glider` (the Glider subtree root) is **not** derived from wherever the
launcher process happens to be running when it starts — see §2 for why
that matters. `<container-id>` is the same 16-hex-character ID
(`process.NewContainerID`) already used for the container's state
directory, strictly validated before ever being used as a path component
(`cgroup.validateContainerID`) — this package treats cgroup paths as
privileged kernel control interfaces, and a container ID can reach it from
an operator-typed `glider-runtime recover <id>` argument, not only from
internally generated values.

## 2. Delegation

cgroup v2 enforces a "no internal process" constraint: a cgroup cannot
both have direct member processes and distribute controllers to its own
children via `cgroup.subtree_control` — attempting to enable a controller
while the cgroup itself has member tasks fails with `EBUSY`. This was
verified empirically against this project's Linux test environment before
designing around it (a plain `bash -c '...'` process moving itself before
attempting to enable subtree_control succeeds; leaving itself in place and
attempting it directly fails with exactly this error).

On a real, systemd-managed production host, this is normally a non-issue:
systemd itself already keeps the true cgroup root (and every intermediate
slice/scope it manages) free of direct member processes, with controllers
already delegated down through the tree it owns. Glider's own process
typically only needs to work within whatever subtree it's already been
placed in.

For the case where that isn't already true (a bare/test environment, or an
operator invoking `glider-runtime` directly from an interactive shell),
Glider bootstraps its own delegation once, idempotently
(`Manager.EnsureDelegated`):

```text
mkdir <cgroup2-mount>/glider/_supervisor          (idempotent)
move THIS process's own pid into _supervisor       (never anyone else's pid)
enable cpu,memory,pids in <cgroup2-mount>/cgroup.subtree_control
enable cpu,memory,pids in <cgroup2-mount>/glider/cgroup.subtree_control
```

Moving the calling process into `_supervisor` (a child of the Glider
subtree root, not the subtree root itself) vacates **both** the true
cgroup root and `glider/` of direct member processes in a single atomic
write, satisfying the "no internal process" constraint for both levels at
once. `_supervisor` is a permanent, shared fixture — multiple concurrent
`glider-runtime` processes safely coexist in it (a cgroup can hold many
processes; this is the same convention systemd itself uses for
`init.scope`) — it is never removed, and its presence is not treated as a
leak.

**What Glider will never do**: move any process it doesn't own. If the
true cgroup root (or `glider/`'s parent) has *other*, non-Glider processes
directly in it that can't be relocated, `EnsureDelegated` fails with
`cgroup.ErrNotDelegated` and the container launch fails clearly — matching
ADR-0001's "fail fast, don't silently degrade" precedent, extended from
"cgroup v2 exists" to "cgroup v2 exists **and is usable**". This is also
why the reproducible test harness
([scripts/test-linux-runtime.sh](../../scripts/test-linux-runtime.sh))
performs this same bootstrap once, itself, before invoking `go test`: a
`docker run --privileged ... bash -c "go test ..."` invocation nests
`bash` → `go test` → `glider-runtime` all sharing the true cgroup root
initially, and only the outermost process (the harness script) is
guaranteed to be root's sole occupant at that point — this is a test
*harness* concern, never something `cmd/glider-runtime` itself performs
in production.

## 3. Naming and path stability

The Glider subtree root is fixed (`<cgroup2-mount>/glider`), **not**
derived from the calling process's own ambient cgroup. This is a
deliberate correctness requirement, not a style choice: recovery
(`process.Recover`) must be able to re-derive the exact same cgroup path
for a given container ID from a *different* process, potentially started
from a *different* ambient cgroup than the original launcher (an operator
running `glider-runtime recover` from a different shell/session entirely).
If the subtree root depended on "wherever this process happens to start",
two different processes could disagree about where a given container's
cgroup lives, breaking recovery outright. A container's path is therefore
a pure function of its ID: `Manager.ContainerPath`/`ContainerPathRelative`,
no state lookup required (though the state record also carries
`CgroupPath` for observability — see §7).

## 4. Resource model

```go
type Resources struct {
    CPUCores    float64 // <= 0 means unlimited
    MemoryBytes int64   // <= 0 means unlimited
    PIDsMax     int64   // <= 0 means unlimited
}
```

CLI flags: `--cpus <n>`, `--memory <size>`, `--pids <n>` on
`glider-runtime run`. Every container gets **all three** limit files
written explicitly — `cpu.max`, `memory.max`, `pids.max` — regardless of
whether a flag was given; an unset flag writes `max` (unlimited)
explicitly rather than leaving the file at whatever it would otherwise
default to. This is a deliberate "prefer explicit semantics" choice: every
container's resource posture is fully legible from its cgroup files alone,
with no implicit inheritance to reason about.

### `--cpus`: fractional CPU bandwidth

A `cpu.max` hard bandwidth limit, using a **fixed 100ms period**
(`cpuPeriodUsec`), matching Docker's and Kubernetes' own convention for
this value — deviating from it would make Glider's fractional-CPU numbers
behave differently from what operators coming from those tools already
expect, for no benefit at this phase's scope. `--cpus 0.5` → quota 50000,
period 100000 (`cpu.max` contents: `"50000 100000"`). `--cpus 0` is
rejected (not treated as "explicitly unlimited" — omit the flag for that);
negative, `NaN`, `Inf`, and values exceeding a 1024-core sanity ceiling
are rejected before any cgroup is touched. A quota that would round below
1000 microseconds (an extremely small fraction) is floored to 1000 rather
than silently becoming 0, which cgroup v2 rejects outright.

**Deferred, not implemented**: `cpu.weight` (relative scheduling priority,
a fundamentally different mechanism from a hard bandwidth cap) and CPU
pinning/`cpuset`. Phase 4's mandate is hard containment; a weight-based
knob is a legitimate future addition with no current caller need.

### `--memory`: hard memory limit

A `memory.max` hard limit. Accepted forms: a bare integer (bytes), or an
integer immediately followed by one of the binary (IEC) suffixes `Ki`,
`Mi`, `Gi` (1024-based — e.g. `256Mi` = 268435456 bytes). Deliberately
**not** decimal suffixes (`K`/`M`/`G`) or Docker-style ambiguous
shorthand (`m`/`g`) — accepting a unit that could be read two different
ways is exactly the ambiguity Phase 4 requires rejecting outright, not
guessing at. `0` and negative values are rejected; a value overflowing a
sane byte-count ceiling is rejected.

**Deferred, not implemented**: `memory.high` (soft throttling — a
different mechanism from hard containment, evaluated but not needed for
this phase's mandate) and any swap policy.

### `--pids`: task count limit

A `pids.max` hard limit on tasks (processes **and threads**) in the
container's cgroup. **glider-init itself consumes one slot** of whatever
limit is configured — it is a member of the same cgroup as the workload
(§5) — this is not hidden from the accounting; a `--pids 1` container
could never actually start a workload, since glider-init alone already
uses the one available slot. `0` is rejected outright (rather than
silently meaning "unlimited", which is instead what omitting the flag
does) — a `--pids 0` container could never contain even glider-init, so
treating it as a valid "unlimited" synonym would be actively misleading.

## 5. Cgroup membership: glider-init and the workload share one cgroup

**Decision**: glider-init, the workload, and every descendant it forks all
live in the **same** container cgroup. This is the simplest and strongest
model available and was adopted without a serious alternative: splitting
supervisor and workload into separate cgroups would mean the resource
boundary no longer represents "the whole container" (contradicting the
project's own framing of a container as one accountable unit), while
adding real complexity (nested cgroup trees, a second set of limit files
to reason about) for no corresponding benefit at this phase's scope.

**Consequence, stated plainly**: glider-init's own (very small) CPU/memory
footprint counts against the container's configured resources. This is
judged acceptable — glider-init's steady-state footprint is minimal
(mostly blocked in a `select` on signals/`SIGCHLD`) — and is the same
tradeoff every mainstream container runtime with an init-style supervisor
makes.

**Attachment ordering** (the critical Phase 4 invariant: *a workload must
not run before Glider has established cgroup membership and limits*):

```text
launcher:
  compute container cgroup path (deterministic from ContainerID)
  record CREATING (durable, before any resource exists)
  EnsureDelegated (idempotent bootstrap, §2)
  Create container cgroup + write cpu.max/memory.max/pids.max
      ↓ (host-side only; glider-init does not exist yet)
  clone() glider-init (new namespaces, still in the launcher's own cgroup)
  wait for glider-init's "ready" (mount setup complete)
  Attach glider-init's host PID to the container cgroup
  VerifyAttached (real /proc/<pid>/cgroup evidence, not just a
                  successful write — §14 of the phase brief)
  record CREATED (durable)
      ↓
  signal glider-init "go"
  glider-init: pivot_root, then fork+exec the workload
```

Because cgroup membership is inherited automatically on fork, the
workload — forked by glider-init only *after* glider-init itself is
already a cgroup member with limits already configured — lands inside the
correctly-configured cgroup with **no separate attachment step**. This is
verified with real kernel evidence in the integration suite
(`TestContainerGetsDedicatedCgroupWithCorrectMembership` reads
`/proc/<pid>/cgroup` for both glider-init and the resolved workload PID),
not merely assumed from the ordering being correct on paper.

### `/proc/self/cgroup` from inside the container

glider-init is cloned with `CLONE_NEWCGROUP` (already part of Phase 1's
namespace flags — unchanged). A cgroup namespace's "root" (what `/proc/
self/cgroup` shows as `/`) is fixed at the moment of `clone()`, to
whatever cgroup the process is a member of *then* — which is still the
launcher's own cgroup, since the move into the container cgroup happens
*after* clone (host-side, using glider-init's now-known host PID; nothing
else was possible without a raw `clone3()`/`CLONE_INTO_CGROUP` reimplementation
of process creation, see below). Consequently, `/proc/self/cgroup` as
observed *from inside* the container does not show a clean `0::/`; it
shows a relative path with leading `../` segments back to the launcher's
own original cgroup. This is a **cosmetic** limitation — cgroup
*membership and enforcement* are entirely unaffected by what the cgroup
namespace's display path looks like — documented here rather than left
silently unverified.

**Considered and not adopted**: Linux 5.7+'s `clone3()` with
`CLONE_INTO_CGROUP` places a new process directly into a target cgroup
atomically at `clone()` time, combined with `CLONE_NEWCGROUP`, giving a
clean `0::/` view. Go's standard `os/exec` does not expose this (it uses
the older `fork`/`exec` path internally, not raw `clone3()`), so adopting
it would mean reimplementing process creation with raw syscalls —
significant, fragile, kernel-version-sensitive surface for a purely
cosmetic improvement with no effect on actual isolation or enforcement.
Not worth it at this phase's scope; noted as a legitimate future
improvement if `/proc/self/cgroup`'s display accuracy ever becomes
load-bearing for something (it isn't, today).

## 6. Statistics

`Manager.Stats(containerID)` reads a small set of typed values fresh on
every call — a foundation for a future `glider stats`, not a telemetry
system:

- `cpu.stat`: `usage_usec`, `user_usec`, `system_usec`, `nr_periods`,
  `nr_throttled`, `throttled_usec`.
- `memory.current`, `memory.peak` (optional — absence, e.g. on an older
  kernel/config, is tolerated and reported as 0, not an error),
  `memory.events` (`low`/`high`/`max`/`oom`/`oom_kill`).
- `pids.current`, `pids.events` (`max` — how many forks were refused).

Parsing tolerates unknown fields the kernel might add in the future
(looked up by name from a map, not positional); a *known* field's value
failing to parse as an unsigned integer is treated as real corruption and
returned as an error, not silently skipped.

## 7. Cgroup identity in durable state

`state.Record` gained (schema version 3, up from Phase 2's version 2):

```go
CgroupPath string             // relative to the cgroup2 mount, e.g. "glider/<id>"
Resources  state.Resources    // the requested limits (dependency-free shadow of cgroup.Resources)
```

`CgroupPath` is recorded for observability/auditability — an operator or
test reading the state file can see exactly which cgroup was allocated
without re-deriving it. It is **not** trusted as the source of truth by
recovery or cleanup: both always re-derive the path deterministically from
`ContainerID` (§3), consistent with container-lifecycle.md's standing
principle that the state file records *intent*, not *proof*. `Resources`
is the plain, dependency-free value the launcher actually requested;
`cgroup.Resources` (in the Linux-only cgroup package) is the single source
of truth for what the fields *mean* (units, the "`<=0` means unlimited"
convention, validation) — `state.Resources` is only its durable shadow, so
the OS-independent `state` package doesn't need to import Linux-only code
merely to borrow a struct shape.

## 8. Cleanup and recovery

### Normal exit

Unlike Phase 1/2's mounts (which self-clean via the kernel's mount
namespace teardown once every process that was ever inside is gone — no
explicit action needed), a cgroup does **not** self-remove; an explicit
`rmdir` is required. Leaving that to an explicit, separate `recover`
invocation (as Phase 2 left `DELETING` for `EXITED`/`FAILED` state
records) would mean **every ordinary container run leaks a cgroup by
default** — clearly wrong, and explicitly tested against (`no cgroup
leaks under stress`, §11). The launcher itself therefore removes the
container's cgroup as its own last step, immediately after confirming
glider-init's process has exited (`finishAfterInitExit` in launcher.go) —
by that point every process that was ever a member is confirmed gone, so
removal is safe and (per `WaitUnpopulated`'s bounded poll of
`cgroup.events`'s `populated` field, not a fixed sleep) essentially
immediate. This is a host-resource action, decoupled from the container's
*state record* — the record still stops at `EXITED`, matching Phase 1/2's
documented scope; only the cgroup directory itself is proactively removed.

### Crash recovery

`cleanup.go`'s `cleanupContainer` (invoked by `process.Recover`'s
`DELETING` handling — unchanged control flow from Phase 2, extended in
scope) now also removes the container's cgroup, using the same
bounded-`WaitUnpopulated`-then-`Remove` sequence. Recovery's existing
identity-validation discipline (container-lifecycle.md §5) extends
naturally: `ensureInitTerminated` runs first, so by the time cgroup
cleanup is attempted, the owning process is already confirmed dead (or was
never alive to begin with, for a crash before `CREATED`). Per
docs/adr/0006's kernel-liveness-equivalence property (runtime.md §8.6):
once glider-init is confirmed gone, the entire PID namespace — and
therefore every process that could have been a cgroup member — is
unconditionally gone too, so the cgroup should already be unpopulated by
the time recovery reaches it; the bounded wait exists for the small window
the kernel needs to finish reaping, not because population is expected to
persist.

**Never**: recovery does not remove or signal anything based on a cgroup
path alone. Ownership is established the same way process identity is —
derived from a validated `ContainerID`, confined to the fixed Glider
subtree (§3), never an arbitrary path from untrusted input.

### Recovery matrix (extends container-lifecycle.md §4)

| Crash boundary | Outcome |
|---|---|
| After cgroup creation, before `CREATED` | Cgroup exists, no live owner recorded yet (or recorded but dead) → recovery converges `CREATING`/`CREATED` to `FAILED`, then (next call) `DELETING` removes the abandoned cgroup. Tested: `TestRecoveryRemovesCgroupAbandonedBeforeCreated`. |
| After `CREATED`, launcher dies, container still `RUNNING` | glider-init (and its cgroup membership) survives independently (runtime.md §8.6) — recovery reports `STILL_HEALTHY`, cgroup untouched. Tested: `TestLauncherCrashWhileRunningCgroupSurvivesUntilRecovered`. |
| glider-init genuinely gone while state still says `RUNNING`/`STOPPING` | Converges to `EXITED` (inferred), next call removes the cgroup. |
| Crash mid-`DELETING` | Idempotent: cleanup tolerates "cgroup already gone" as success, re-runs safely. |

## 9. I/O — deferred

The I/O controller (`io.max`) is **not** implemented in Phase 4. Reliable
enforcement requires mapping the container's actual backing block device
(`major:minor`), which this project's real verification environment
(Docker Desktop's LinuxKit VM, itself virtualized, running on an
overlay/virtual backing store) cannot honestly provide a stable,
verifiable mapping for. Writing an `io.max` file that cannot be proven to
throttle anything real would be exactly the "claim it works based only on
successfully writing a control file" outcome the phase brief explicitly
rejects. Deferred to a later hardening milestone with a verification
environment that can back it honestly — not attempted here.

## 10. Known limitations

- `/proc/self/cgroup`'s displayed path from inside a container leaks a
  relative-path fragment of the host's own cgroup topology (§5) —
  cosmetic only; membership and enforcement are unaffected.
- I/O is not enforced (§9).
- `cpu.weight` and `memory.high` are not exposed (§4) — both are legitimate
  future additions, not needed for this phase's hard-containment mandate.
- `EnsureDelegated`'s bootstrap dance assumes it may need to move *this
  process's own* PID; it never touches any other process, so a host where
  the true cgroup root (or wherever Glider starts) is shared with
  unrelated, non-Glider processes that can't be relocated will correctly
  fail with `ErrNotDelegated` rather than degrading — by design, not a gap
  to fix, but worth stating plainly since it does mean cgroup support is
  not universally guaranteed on every conceivable host topology.
