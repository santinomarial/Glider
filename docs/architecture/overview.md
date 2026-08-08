# Glider architecture overview

Status: living document, Phase 0.
Related: [container-lifecycle.md](../design/container-lifecycle.md),
[runtime.md](../design/runtime.md), [failure-model.md](../design/failure-model.md),
[security-model.md](../design/security-model.md).

## 1. What Glider is

Glider is a distributed system for running containerized workloads across a
small cluster of Linux machines, built from Linux primitives (namespaces,
cgroup v2, OverlayFS, netlink) and a reconciliation-driven control plane
(etcd). It is scoped as a deliberately smaller, self-implemented combination
of ideas from runc, containerd, and Kubernetes — not a wrapper around any of
them.

Two binaries carry almost all of the system's behavior:

- **Control plane** (API server, scheduler, controllers) — decides *what
  should run and where*, and durably records that decision.
- **`gliderd`** (one per node) — makes the node's *actual* state match the
  control plane's *desired* state, using a self-contained runtime, image
  store, and network stack.

Everything else (CLI, client library, benchmarking/chaos harnesses) is a
consumer of these two.

## 2. Design philosophy

These principles are load-bearing — later phases are expected to violate a
naive reading of section 6 of the master plan only if this document is
updated first, via an ADR if the change is one we intend to freeze.

**Desired state, not imperative orchestration.** The control plane never
tells a node "start container X now." It records "task X should be RUNNING,
generation N" and `gliderd` reconciles toward that. There is no code path
where an imperative RPC is the sole record of an action — if it isn't in
durable desired state, it didn't happen as far as the rest of the system is
concerned.

**Idempotence.** Every operation that mutates node-local or cluster state is
expressed as `Ensure*(target_state)`, not `Do*(action)`. Calling
`EnsureContainerExists(task, generation)` twice must produce one container,
not two, whether the second call is a genuine retry, a duplicate delivery, or
a reconciliation loop re-running because it doesn't remember it already ran.

**Explicit state machines.** Container, node, task, and assignment lifecycles
are enumerated below and in `container-lifecycle.md`. No component is allowed
to infer "the container is basically running" from partial evidence; it is
either in a recorded state or it is being actively determined.

**Strong invariants.** Each subsystem's design doc states what must always be
true of a given state (e.g. "RUNNING implies the cgroup, netns, and init
process all exist and the recorded PID's start time matches"). Tests attempt
to falsify these, not just exercise the happy path.

**Crash consistency.** Every design must answer: what does this component do
if it is killed between step N and N+1? Sequencing is chosen so that a
restart can either resume, safely redo (idempotently), or safely discard
partial work — see `failure-model.md`.

**No fake exactly-once execution.** A partitioned node cannot be proven to
have stopped a process before the control plane reschedules it elsewhere.
Glider does not claim exactly-once execution. It claims: durable assignment
identity, monotonic generations, lease-bounded authority, and fencing that
rejects stale generations. See §5.4 and `failure-model.md`.

## 3. End-to-end path

```text
glider CLI  --(gRPC)-->  API server
                             │
                    validate + persist spec
                             │
                            etcd  (desired state, source of truth)
                             │
                    workload controller (watch)
                             │
                    creates/updates Task objects (desired replica slots)
                             │
                          scheduler (watch pending tasks)
                             │
                filter candidate nodes -> score -> pick one
                             │
              atomic bind: etcd compare-and-swap creates Assignment
              (task_id, generation N, node_id) iff task was unbound
                             │
                            etcd
                             │
                 gliderd on node_id (watch assignments for this node)
                             │
                 reconcile loop: observed vs desired for this task
                             │
        ensure image (pull, verify digest, unpack) -> content store
                             │
                 ensure OverlayFS root (lower=image layers, upper=container)
                             │
                 ensure network (netns, veth, bridge attach, IP)
                             │
        ensure namespaces + cgroup v2 + capabilities + seccomp + no_new_privs
                             │
                 launch container init (PID 1 in its namespaces)
                             │
                 report observed state (container RUNNING, generation N)
                             │
                            etcd  (status, separate from spec)
                             │
              controllers observe status -> re-reconcile if diverged
```

The loop at the bottom never terminates: `gliderd` re-evaluates desired vs.
observed on every watch event and on a periodic resync, so a divergence
introduced by any failure (crash, manual `rm`, OOM kill) is corrected without
a human issuing a new imperative command.

## 4. Identifier model

| Identifier | Generated | Scope | Durable? | Format intent |
|---|---|---|---|---|
| `ClusterID` | once, at `glider cluster init` | cluster | yes | random (UUIDv4) |
| `NodeID` | once, at first `glider node join`, persisted locally before the node ever contacts the control plane | cluster | yes | random (UUIDv4) — **never** derived from hostname or IP, both of which can change or collide |
| `WorkloadID` | at workload creation | cluster | yes | random, paired with a user-chosen unique `name` (human key) |
| `TaskID` | at task creation by the workload controller | cluster | yes | `<workload-name>-<ordinal>`; identifies a replica *slot*, not a specific process |
| `(TaskID, Generation)` — assignment identity | generation increments on every new binding decision for a task | cluster | yes | `Generation` is a per-task monotonic counter; this pair is the fencing token (§5.4) |
| `ContainerID` | by `gliderd`, per launch attempt | node-local | yes (node-local disk) | `<task-id>/<generation>/<attempt>` — `attempt` increments on local restart (e.g. crash-loop) *without* a new assignment generation |
| `ImageDigest` | content hash | cluster-wide (content-addressed) | yes | `sha256:<hex>` per OCI digest spec; identity *is* the content, never reassigned |
| `RequestID` | per RPC | ephemeral | no | correlation only, not persisted as authoritative state |

The `ContainerID` hierarchy is deliberate: it lets `gliderd` distinguish "the
same assignment restarted locally after a crash" (bump `attempt`, same
`generation` — the node still owns this work) from "the control plane moved
this task elsewhere" (new `generation` — the old container must be torn down
and is no longer authoritative even if still technically running).

## 5. Control-plane state machines

These are the desired-state-side lifecycles. The container's own local
runtime lifecycle (what `gliderd` drives on a single node, including PID
reuse and crash-recovery detail) is specified separately in
[container-lifecycle.md](../design/container-lifecycle.md) because it has
enough Linux-specific nuance to deserve its own document. The two are
related but distinct: a Task can be `RUNNING` at the control-plane level
while, locally, `gliderd` is mid-way through `CREATING` a replacement
container after a crash.

### 5.1 Node lifecycle

```text
                 join succeeds, lease acquired
   JOINING ─────────────────────────────────────► READY
                                                     │  │
                                    heartbeat missed │  │ operator drain
                                        (soft, N misses)│
                                                     ▼  │
                                                 SUSPECT │
                                                     │   │
                                lease renewed        │   │
                              before expiry ◄─────────┘   │
                                                     │     │
                                          lease expires    │
                                                     ▼     ▼
                                            UNREACHABLE  DRAINING
                                                     │     │
                                    operator confirms │     │ all tasks
                                    removal / node    │     │ evacuated
                                    rejoins with new  │     │
                                    lease + self-fence│     │
                                                     ▼     ▼
                                                  REMOVED (terminal, or
                                                  rejoin re-enters JOINING
                                                  with a fresh NodeID lease)
```

- **JOINING**: node has a persisted `NodeID` and is establishing its lease
  with the control plane; not yet schedulable.
- **READY**: lease is current; scheduler may place tasks here.
- **SUSPECT**: heartbeats are late but the lease has not yet expired. This is
  purely a scheduling signal — *new* placements are avoided, but existing
  assignments remain valid and are **not** fenced. A single missed heartbeat
  must not trigger rescheduling; see `failure-model.md` for why (detection
  latency vs. false-positive churn is a tunable, not a hardcoded assumption).
- **UNREACHABLE**: the node's lease expired. Its assignments become eligible
  for fencing and reschedule (§5.4). This is a control-plane-observed state;
  it is not proof the node's processes have stopped.
- **DRAINING**: operator-initiated, orthogonal to health. Existing tasks are
  rescheduled elsewhere deliberately; new placements are refused.
- **REMOVED**: terminal for that `NodeID`. A physical machine that rejoins
  after being marked `UNREACHABLE` or `REMOVED` gets a fresh lease and must
  reconcile its locally-running containers against current assignment
  generations before resuming anything (self-fencing, §5.4) — it does not
  get to assume its old work is still valid.

This is a working model, not frozen — SUSPECT's exact threshold semantics
are revisited once real heartbeat behavior is measured (Phase 13).

### 5.2 Task lifecycle

A Task is a desired replica slot owned by the workload controller. It is
control-plane state, not a process.

```text
PENDING ──(scheduler binds)──► SCHEDULED ──(gliderd reports RUNNING
   ▲                               │           for current generation)──► RUNNING
   │                               │                                         │
   │        assignment fenced      │                                         │
   │        (node lost, §5.4) ─────┴─────────────────────────────────────────┤
   │                                                                         │
   └─────────────────────── rescheduled: new generation, back to PENDING ◄──┘
                                                                              │
                                                  desired replicas decreased, │
                                                  or workload deleted        │
                                                                              ▼
                                                                        TERMINATING
                                                                              │
                                                                       node confirms
                                                                       container gone
                                                                              ▼
                                                                        TERMINATED
```

A Task's status is an aggregate of what `gliderd` reports about the
container(s) run under its current (and immediately prior, during handoff)
generation — never something the node writes directly as final truth. The
node reports *observed* container state; the control plane owns the
interpretation of what that means for the Task.

### 5.3 Assignment lifecycle

An Assignment binds one `(TaskID, Generation)` to one `NodeID`. This is the
object the atomic scheduling transaction (§21/§22 of the master plan,
detailed later in `docs/design/scheduling.md` once Phase 12 begins) actually
creates.

```text
UNBOUND ──(scheduler CAS bind)──► BOUND(gen N, node X)
                                        │
                    ┌───────────────────┼───────────────────┐
                    ▼                   ▼                   ▼
              CONFIRMED            REJECTED            SUPERSEDED
         (node acked, started    (node declines —    (a bind for gen N+1
          container under gen N)  e.g. resources     was created before
                    │              didn't actually    node confirmed gen N;
                    │              fit on arrival)     happens under races)
                    │                   │                   │
                    └─────────► task returns to PENDING for a fresh bind ◄──┘
                    │
                    ▼
                 FENCED
         (control plane declares node's lease expired;
          generation N is no longer executable by anyone,
          including the node itself if it reappears)
```

Invariant: **at most one non-superseded, non-fenced generation is
executable, per task, at any moment.** `gliderd` must check this before
acting (see §5.4) rather than trusting that whatever it was told to run is
still current.

### 5.4 Generations and fencing (why, briefly)

A network partition cannot be distinguished, at the moment it happens, from a
crash. If Glider reschedules a task after a lease expires, and the original
node was actually alive but partitioned, both the original node and the
replacement node might now believe they should be running the workload. This
is unavoidable in an asynchronous network with fail-stop assumptions removed
— it is not a bug to be engineered away, so Glider does not pretend
otherwise (§2.6 above).

What Glider *can* guarantee: the control plane will never accept a report
from, or a re-bind based on, a generation older than the one it already
considers current for a task. And a well-behaved node self-fences — if it
cannot renew its lease before an uncertainty deadline, it stops workloads it
holds under that lease rather than waiting to be told. This bounds the
*window* of possible double-execution to the self-fencing deadline; it does
not eliminate it. Full protocol detail (exact deadlines, message formats) is
specified when Phase 13 begins and becomes ADR-0007; this section fixes only
the safety property the later design must satisfy.

## 6. Component map

```text
control plane (replicated, leader-elected where stateful in-memory
work is involved — e.g. the scheduler's binding loop)
    ├─ API server        gRPC surface, spec validation, versioning boundary
    ├─ workload controller   desired replicas -> Task objects
    ├─ node controller       lease tracking -> Node lifecycle (§5.1)
    ├─ rollout controller    generation changes -> phased Task replacement
    ├─ service controller    selector -> endpoint set (healthy Tasks only)
    ├─ scheduler          pending Tasks -> filter -> score -> atomic bind
    └─ etcd               durable desired state, status, leases, CAS, watch

gliderd (per node, independent, no direct node-to-node coordination)
    ├─ runtime        namespaces, pivot_root, cgroup v2, capabilities, seccomp
    ├─ image store     content-addressed blobs, manifest/config, layer unpack
    ├─ snapshotter     OverlayFS lower/upper/work per container
    ├─ network agent   netns, veth, bridge, IPAM, (later) VXLAN
    └─ reconciler      watch assignments for this node, drive Ensure* calls
```

`gliderd` never talks to another `gliderd` directly in the single-node and
early multi-node design; all coordination is mediated by the control plane
and etcd. Direct node-to-node dataplane traffic (container-to-container
packets) is the one deliberate exception, starting at Phase 14.

## 7. Non-goals

See master plan §36. Restated as an active filter for design decisions in
this repo: no CRI/CNI/Kubernetes-API compatibility, no custom Raft, no
distributed block storage, no service mesh, no autoscaling, no multi-cluster
federation, no checkpoint/restore. If a design doc in this repo starts
depending on one of these, that is a signal the doc has drifted from scope,
not that the non-goal should quietly be dropped.
