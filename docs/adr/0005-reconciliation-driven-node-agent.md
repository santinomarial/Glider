# ADR 0005: reconciliation-driven node agent

## Status

Accepted.

## Context

`gliderd` must bring a node's actual state (running containers, networks,
cgroups) into agreement with the control plane's desired state
(assignments), and keep it that way as failures, restarts, and scaling
events happen (master plan §18, §27; overview.md §2).

## Problem

Two shapes are possible for how the control plane tells a node what to do:

1. **Imperative RPC**: the control plane calls `StartContainer(task)` on
   the node when it decides to; the node executes it once, as an action.
2. **Reconciliation (level-triggered)**: the control plane durably records
   "this task should be RUNNING here, generation N"; the node watches that
   state and continuously drives its local reality toward it, independent
   of *why* the desired state changed.

Imperative RPC is simpler to write for the happy path but degrades badly
under the failures this project explicitly designs for (failure-model.md):
a retried or duplicated RPC risks double-starting a container unless every
RPC handler is independently made idempotent; a node that misses an RPC
(crashed, partitioned) while it fired needs a separate reconciliation-like
sweep bolted on anyway to catch up — meaning a purely imperative design
tends to grow an ad hoc reconciliation mechanism on the side once real
failures are handled, rather than having one coherent mechanism from the
start.

## Decision

`gliderd` is **level-triggered and reconciliation-driven**, matching
overview.md §2's "desired state, not imperative orchestration" principle.
It watches the set of assignments bound to its `NodeID` in etcd and, on
every watch event *and* on a periodic resync (to correct drift no watch
event announced — e.g. a container someone `kill -9`'d out from under it),
runs the same reconcile pass: compare desired (assignment generation,
task spec) against observed (local container-lifecycle.md state) and issue
whatever `Ensure*` calls close the gap. There is no separate "handle this
one RPC" code path with different semantics from "catch up after a
restart" — they are the same function.

## Alternatives considered

- **Pure imperative RPC**, as described above. Rejected: pushes the burden
  of idempotence and catch-up-after-miss onto ad hoc per-RPC logic instead
  of one coherent loop, and doesn't naturally handle the case where desired
  state changes for a reason other than an explicit RPC (e.g. a fenced
  assignment, master plan §25) — reconciliation handles that uniformly,
  imperative RPC would need a bespoke path for it.
- **Hybrid: RPC for the common case, periodic reconciliation as a
  backstop.** Considered, and in fact close to what's decided — but
  explicitly *not* as two different mechanisms with different code paths.
  The watch event and the periodic resync both feed the same reconcile
  function; there is no separate "fast path" with weaker idempotence
  guarantees than the backstop. Naming this explicitly because it's the
  natural way this decision could quietly erode into two mechanisms if not
  stated.
- **Push-based streaming command channel** (control plane pushes a
  continuous stream of state to each node, node applies it as a sequence).
  Rejected as materially more complex than watch-plus-resync against a
  keyspace etcd already provides, for no corresponding benefit — etcd's
  watch already is a push mechanism; layering another one on top of it is
  redundant.

## Consequences

- Every `gliderd`-side operation must be an `Ensure*` (idempotent,
  convergent) rather than a `Do*` (one-shot action) — this is now a coding
  discipline enforced by this ADR, not just a style preference (overview.md
  §2).
- A `gliderd` crash mid-operation is recoverable by the same reconcile pass
  running again after restart, using on-disk state as ground truth
  (container-lifecycle.md §4) — no separate crash-recovery code path is
  needed beyond "run reconciliation again."
- The periodic resync interval is a tunable that trades detection latency
  for control-plane/etcd watch load; the exact value is a Phase 10
  implementation decision, not fixed here.

## Risks

- A reconcile pass that is not carefully idempotent could still cause
  duplicate side effects; this ADR doesn't automatically make individual
  `Ensure*` implementations correct, it only commits to the architecture
  that makes idempotence the right (and required) property to build toward
  — verified per-operation in Phase 10's tests, not by this ADR alone.

## What would cause reconsideration

If reconciliation-pass latency under real watch/resync load proves too
slow for acceptable convergence time at target scale (measured in Phase 19
benchmarking, not assumed now), the resync strategy (interval, batching)
would be revisited — the level-triggered architecture itself would not be,
since the alternative (imperative RPC) has the correctness problems
described above regardless of performance.
