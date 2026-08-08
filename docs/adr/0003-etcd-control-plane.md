# ADR 0003: etcd for control-plane state

## Status

Accepted.

## Context

The control plane needs durable, replicated storage for desired state
(workloads, tasks, assignments) and status, plus the coordination
primitives a scheduler and controllers need: atomic compare-and-swap,
watch/notify, and leases for node liveness (master plan §22, §24, §25).
This requires a consensus system somewhere underneath it.

## Problem

Building that consensus system is itself a large, well-studied distributed
systems problem (leader election, log replication, snapshotting, membership
changes). Master plan §36 lists "custom Raft" as an explicit non-goal, and
§4 states plainly: use etcd, do not implement another Raft system inside
Glider.

## Decision

Use **etcd** as the sole replicated store for control-plane desired state,
status, and coordination primitives. Glider's control plane is a client of
etcd, not a consensus implementation. Specifically, Glider relies on:

- **Transactions** (`Txn` with `Compare`/`If`/`Then`/`Else`) for atomic
  task-binding (overview.md §5.3) and any other read-check-write that must
  not race.
- **Revisions** as the ordering mechanism for "what happened before what,"
  per failure-model.md §2 — not wall-clock timestamps.
- **Leases** for node liveness (overview.md §5.1) and for scoping any
  ephemeral registration data.
- **Watch** for controllers and `gliderd` reconciliation loops to react to
  state changes without polling.

## Alternatives considered

- **Custom Raft** (`hashicorp/raft` or hand-written) embedded directly in
  Glider's control-plane binary. Rejected per master plan §4/§36: the
  project's value is in runtime/scheduling/reconciliation/failure-handling
  depth, not in re-deriving consensus correctness, which etcd already
  provides as a mature, battle-tested implementation of exactly the
  primitives needed (CAS, watch, lease).
- **Consul.** Considered as a similar off-the-shelf option; rejected in
  favor of etcd because etcd's data model (a flat key-value space with
  explicit revisioning) maps more directly onto the CAS-based
  atomic-binding design (§22) than Consul's session/catalog-oriented model,
  and etcd is the same choice Kubernetes made for the same class of
  problem, which is a reasonable prior for a project deliberately inspired
  by Kubernetes' control-plane shape.
- **A relational database with row-level locking** (e.g. Postgres
  `SELECT ... FOR UPDATE`) instead of a purpose-built coordination store.
  Rejected: gains SQL familiarity but loses native watch/lease primitives
  that the reconciliation-driven design (ADR-0005) depends on; would need
  to be reimplemented (polling or LISTEN/NOTIFY approximations) on top,
  which is worse than using a store built for this.

## Consequences

- Glider's control plane has a hard runtime dependency on an etcd cluster
  (itself typically 3 or 5 members for quorum) — this is an operational
  dependency the project accepts deliberately, documented as such rather
  than hidden.
- Key design (the etcd keyspace layout for workloads/tasks/assignments/
  leases) becomes a first-class artifact once Phase 11 begins; this ADR
  fixes *that etcd is used*, not yet the keyspace schema.
- etcd cluster failure modes (member loss, quorum loss) become part of
  Glider's own failure model (failure-model.md, master plan §32 "etcd
  member failure" chaos scenario) — Glider must degrade sensibly (e.g.
  reject writes, keep serving reads it can) rather than assume etcd is
  always available.

## Risks

- Running etcd correctly (backup, quorum sizing) is itself an operational
  skill; for a project scoped to a small demo cluster this is manageable,
  but it's worth being explicit that etcd is now "part of the system,"
  not an invisible detail.

## What would cause reconsideration

None currently anticipated within this project's scope — this decision is
explicitly endorsed by the master plan and aligns with keeping project
effort on the parts unique to Glider.
