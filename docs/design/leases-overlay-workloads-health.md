# Leases, overlay networking, workloads, and health (Phases 13–16)

## Phase 13: leases, generations, and fencing

Every `gliderd` instance acquires exactly one etcd lease-backed ownership key
for its stable node ID. A second process cannot acquire the same live node.
Renewal failure does not immediately declare the node dead: the agent retains
authority through a configurable uncertainty window. If authority still cannot
be proven at `--self-fence-after`, it cancels reconciliation and serially
removes all managed assignments before exiting.

The control-plane monitor progresses `READY → SUSPECT → UNREACHABLE`, restoring
a node to `READY` if its lease returns before the grace period. When a node
becomes unreachable, assignment deletion, task requeue, and reservation release
use etcd CAS transactions. A later bind increments the assignment generation;
agents already reject any desired generation older than their durable record.
This favors safety during partitions and deliberately does not claim
exactly-once execution.

## Phase 14: VXLAN node-subnet overlay

Nodes advertise non-overlapping `pod_cidr` and routable `tunnel_address`
values. `gliderd` periodically supplies the complete READY-peer snapshot to the
network manager. It level-triggers `glider-vxlan` (VNI 64, UDP 4789), attaches
it to `glider0`, installs remote subnet routes and flood-database entries, and
removes stale Glider-owned routes before replacing desired state. The default
MTU is 1450 to reserve encapsulation headroom. The peer snapshot is durably
fsync/rename persisted for audit and restart reconstruction.

## Phase 15: workload replica reconciliation

A versioned Workload contains a desired replica count and Task template. The
controller creates deterministic ordinal Tasks when below desired state and
deletes highest ordinals when above it. Assigned task deletion atomically
removes the assignment and releases the node reservation. The normal scheduler
loop binds pending tasks; unreachable-node eviction returns existing tasks to
that same path, avoiding a separate imperative recovery mechanism.

Workloads are available through `PutWorkload` and `ListWorkloads` on
`glider.v1.ControlPlane`. Replica counts are bounded to 10,000 per workload.

## Phase 16: health and restart semantics

Task templates carry separate startup, liveness, and readiness probes plus one
of `Never`, `OnFailure`, or `Always` restart policies. Startup success gates
normal probing; liveness failure requests restart; readiness changes only
service eligibility and never implies process death. Each probe has independent
success/failure thresholds and a timeout. HTTP accepts 2xx/3xx, TCP requires a
successful connection, and exec probes require an explicitly injected
container-namespace executor—Glider never accidentally runs them on the host.

Restart delays use capped exponential backoff. Task status records readiness,
restart count, and the last health transition so controller behavior is
observable and reconstructible.

The node reconciler is also the authoritative observer for process lifecycle.
It promotes only its current assignment generation from `SCHEDULED` to
`RUNNING`, including a durable start timestamp. A durable runtime exit is
evaluated against `Never`, `OnFailure`, or `Always`: restartable results revoke
the exact assignment and return the task to `PENDING`; terminal results record
the exit code, reason, and finish timestamp while atomically deleting the
assignment and releasing the node reservation. Every report compares both the
task and assignment revisions, so a superseded node cannot revive or complete
newer work.

## Verification boundaries

Embedded-etcd tests race node ownership, concurrent scheduling, transactional
task deletion, reservation release, and generation-increasing node eviction.
Pure state-machine tests cover health thresholds and bounded backoff. VXLAN,
network namespaces, nftables, and self-fencing workload termination still
require the privileged Linux harness (`scripts/test-linux-runtime.sh`) because
macOS cannot execute those kernel mechanisms.
