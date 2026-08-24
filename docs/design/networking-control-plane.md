# Networking and control plane (Phases 9–12)

## Phase 9: node-local networking

`internal/network.Manager` owns a Linux bridge (`glider0`), one veth pair per
container, the container's `eth0` address/default route, and nftables NAT. It
uses netlink APIs directly; it never shells out to `ip`, `nsenter`, or
`iptables`. The runtime invokes networking after the namespace and cgroup exist
but before releasing the workload start barrier.

IPAM and endpoint records live below `/var/lib/glider/network`. Allocation and
kernel/NAT mutation are serialized with advisory locks and records are
fsync/rename published. Endpoint deletion removes the veth and NAT ownership
before releasing the address, preventing stale rules from targeting a reused
IP. Published ports are globally unique per node/protocol. DNS configuration is
written beneath the prepared root filesystem using `openat` and `O_NOFOLLOW`.

Example:

```bash
sudo glider-runtime run --image nginx:alpine --publish 8080:80
```

## Phase 10: node reconciliation

`gliderd` watches assignments for one stable node ID and periodically performs
a full level-triggered resync. Watch closure, etcd errors, and transient runtime
errors are retried; events only wake reconciliation and are never treated as
state. The reconciler durably records assignment generation and observed
container identity. On restart it observes PID+start-time-validated runtime
state before deciding whether to create, replace, or remove a container.

```bash
sudo gliderd --node-id node-a --etcd-endpoints 127.0.0.1:2379
```

## Phase 11: etcd and versioned gRPC API

The authoritative keyspace is
`/glider/v1/clusters/<cluster-id>/{tasks,nodes,assignments}`. Resource revisions
come from etcd `ModRevision`; creates and updates use compare-and-swap. The
`glider-controlplane` process exposes the typed `glider.v2.ControlPlaneService`
and the compatibility-only `glider.v1.ControlPlane` service over gRPC. Bundled
clients use v2. The legacy service uses `google.protobuf.Struct` to carry the
versioned JSON resource model until its documented compatibility window ends;
both the RPC package and each resource's `apiVersion: glider.dev/v1` remain
explicit compatibility boundaries.

```bash
glider-controlplane --listen 127.0.0.1:8443 \
  --etcd-endpoints 127.0.0.1:2379 --cluster-id demo
```

## Phase 12: scheduler and atomic binding

The scheduler filters non-ready/unschedulable nodes, label mismatches,
insufficient CPU or memory, and conflicting host ports. It scores feasible
nodes by balanced remaining capacity, existing image locality, and workload
spread, then applies a stable node-ID tie-break.

A choice becomes authoritative only through one etcd transaction comparing
the task revision, node revision, and absence of an assignment. That same
transaction changes the task to `SCHEDULED`, reserves node resources, and
creates the generation-fenced assignment. Losing schedulers reload all inputs
and retry, so concurrent schedulers cannot double-bind or over-reserve from the
same node revision.

## Failure boundaries

- A persisted IP without a veth is safe to retry; an address is not returned to
  the pool until endpoint and NAT ownership are gone.
- A missed watch is repaired by periodic resync.
- A stale assignment generation cannot replace a newer local generation.
- A scheduler decision without a successful etcd transaction has no authority.
- Runtime process liveness is always checked with PID plus process start time,
  never PID alone.
