# Failure model

Status: Phase 0 design, binding across all phases. The "`gliderd` crash"
row below is implemented ahead of `gliderd` itself (Phase 10) by Phase 2's
`process.Recover` (runtime.md §8.6, container-lifecycle.md §4) — invoked
explicitly today (no daemon/reconciler exists yet to run it
automatically), but the recovery *logic* it names is real, not aspirational.
Related: [architecture/overview.md](../architecture/overview.md) §5.4
(generations and fencing), [container-lifecycle.md](container-lifecycle.md) §4.

This document enumerates what Glider assumes can go wrong, and for each
failure class, whether Glider handles it automatically, detects it without
auto-repair, or explicitly does not address it. Every later design doc
should be checkable against this list rather than inventing its own ad hoc
failure handling.

## 1. Classification

| Failure | Handled (auto-repaired) | Detected, not auto-repaired | Non-goal |
|---|---|---|---|
| Container/workload process crash | yes — restart policy + reconciliation (container-lifecycle.md, Phase 16) | | |
| `gliderd` crash | yes — restart resumes from on-disk state (container-lifecycle.md §4) | | |
| Control-plane process crash | yes — leader election + etcd as source of truth means a new leader resumes from durable state | | |
| Machine crash (whole node) | yes — lease expiry + fencing + reschedule (overview.md §5.4) | | |
| Network partition (node ↔ control plane) | yes, bounded by self-fencing deadline (overview.md §5.4) | the *fact* that a partition (vs. crash) occurred is not distinguishable and Glider does not try to distinguish them | proving the partitioned node has stopped its workload before reschedule elsewhere |
| Message retry / duplicate RPC delivery | yes — idempotent `Ensure*` operations (overview.md §2) | | |
| etcd operation conflict (CAS failure) | yes — retry with fresh read (overview.md §22) | | |
| Disk full (node-local: image store, container state) | | yes — surfaced as node/task status, operator or later capacity-aware scheduling reacts | automatic disk reclamation beyond documented image GC (deferred past Phase 5/6, master plan §13) |
| Partial local filesystem write (crash mid-write) | yes — durable-intent-before-resource ordering (container-lifecycle.md §3) makes recovery a matter of re-reading state, not guessing | | |
| Corrupt image blob | yes — digest verification rejects it before use (master plan §13, §32 "image corruption" chaos scenario) | | |
| Stale node reconnect (rejoin after being marked UNREACHABLE) | yes — self-fencing on rejoin, generation validation before resuming any workload (overview.md §5.4) | | |
| PID reuse | yes — PID + start-time identity tuple (container-lifecycle.md §5) | | |
| Workload exceeds configured CPU limit | yes — cgroup v2 throttles (not kills) the workload; the workload continues running, slower (cgroups.md §4, Phase 4) | | guaranteeing a specific latency/throughput floor under CPU pressure |
| Workload exceeds configured memory limit | yes — the kernel OOM-kills the offending process; Glider reports the container's own termination (128+SIGKILL) rather than collapsing it into a generic failure (cgroups.md §4/§11, Phase 4) | | preventing the OOM kill itself, or choosing *which* process in a multi-process container the kernel kills |
| Workload exceeds configured PID limit | yes — further forks inside the container's cgroup fail (`EAGAIN`-class refusal) while the container's existing processes keep running; this is also Glider's fork-bomb containment (cgroups.md §4) | | |
| Abandoned/leaked container cgroup (crash before cleanup) | yes — `process.Recover`'s `DELETING` handling removes it idempotently, same identity-validated discipline as PID reuse above (cgroups.md §8) | | |
| Slow node (correct but high-latency) | | yes — indistinguishable from partial failure at the heartbeat layer; SUSPECT state (overview.md §5.1) surfaces it without immediately fencing | guaranteeing bounded scheduling latency under an arbitrarily slow node |
| Clock skew between nodes | design avoids depending on synchronized wall clocks for correctness (§2 below) | | tight bounds on lease timing accuracy under large skew |
| Two schedulers racing to bind the same task | yes — atomic CAS bind, loser retries against fresh state (overview.md §5.3, master plan §22) | | |
| Byzantine / malicious node behavior | | | out of scope — Glider assumes fail-stop or fail-partition nodes, not adversarial ones (see security-model.md for the separate, different concern of adversarial *workloads*) |

## 2. Clock assumptions

Glider does not use wall-clock comparison across machines as a correctness
mechanism (e.g. "node A's timestamp is newer than node B's, so trust A").
Ordering that matters for correctness (assignment generations,
fencing) is derived from:

- **etcd's own ordering** (revisions, lease TTLs) for control-plane state —
  etcd already solves the "what happened before what" problem for the data
  it holds, and Glider does not re-derive it from timestamps stored in that
  data.
- **Monotonic counters** (`Generation`, `Attempt`) for identity/precedence
  between competing assignments, not "whichever has the later timestamp."
- **Local monotonic time** (not wall-clock) for a single node's own
  self-fencing deadline — a node measures its own lease's remaining TTL
  using its local monotonic clock against the lease duration it was
  granted, not by comparing timestamps issued by a different machine.

Wall-clock timestamps are still recorded for observability (logs, events,
"last seen" display) — they are just not load-bearing for safety
properties.

## 3. Detected-inconsistency behavior

Several rows above are "detected, not auto-repaired." The common shape:
Glider surfaces the inconsistency as explicit status (a node/task condition,
an event, a metric) rather than either (a) silently papering over it by
rewriting recorded state to match observed reality with no record that
anything was wrong, or (b) crashing/hanging. This mirrors
container-lifecycle.md §2's rule that a state-vs-`/proc` mismatch is
surfaced, not silently "fixed." The general principle: **auto-repair is for
failures whose correct resolution is unambiguous** (a crashed container
under a restart policy, a lost node whose lease has verifiably expired);
**detection-only is for failures whose resolution requires judgment or
capacity Glider doesn't have** (disk full, a persistently slow node,
disagreement discovered post hoc).

## 4. What this document deliberately does not promise

Restated from overview.md §5.4 because it is easy to accidentally violate in
a later design doc under time pressure: Glider does not claim exactly-once
workload execution, does not claim it can prove a partitioned node has
stopped, and does not claim bounded detection latency independent of
configured thresholds. Any later doc proposing behavior that implicitly
assumes one of these should be treated as a design error, not a shortcut.
