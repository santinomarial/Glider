# Glider

Glider is a Linux-native distributed container platform written in Go. It
implements its own container runtime (namespaces, cgroup v2, OverlayFS,
capabilities/seccomp), its own OCI image handling (content-addressed store,
layer unpacker, snapshotter), its own single- and multi-node networking
(veth/bridge/IPAM, VXLAN overlay), and a reconciliation-driven control plane
(etcd-backed desired state, scheduler, node leases, fencing) — rather than
wrapping Docker, containerd, or Kubernetes.

Status: **software production gate green; deployment certification required**.

> [!IMPORTANT]
> Passing the software gate does not certify a specific production cluster.
> Multi-host availability, recovery, monitoring delivery, and independent
> security evidence must be collected from the target environment. See
> [production readiness](docs/release/production-readiness.md).

## Capabilities

- OCI image resolution, digest verification, hostile-tar-safe layer unpacking,
  and OverlayFS snapshots
- Linux namespaces, `pivot_root`, cgroup v2 limits, capability reduction,
  `no_new_privs`, seccomp, and PID 1 supervision
- Bridge/veth networking, persistent IPAM, DNS, NAT, host-port publication,
  VXLAN overlay, service VIPs, and stateful network policy
- Restart-safe node reconciliation backed by etcd desired state
- Resource-aware scheduling with atomic assignment and reservation
- Renewable node leases, monotonic generations, and bounded self-fencing
- Workload replicas, health checks, rolling updates, service discovery,
  encrypted secrets, authenticated node operations, and audit events
- Mutual TLS, role-based authorization, quotas, rate limits, durable
  idempotency, encrypted backups, schema migration, and signed releases

## Architecture at a glance

Operators submit desired state through the mutually authenticated gRPC API.
Controllers and the scheduler persist decisions in etcd. Each `gliderd` watches
only authoritative assignments for its node and idempotently reconciles image,
network, cgroup, namespace, and process state. Start with the
[system context](docs/architecture/system-context.md), then zoom into the
[container view](docs/architecture/container-view.md),
[deployment topology](docs/architecture/deployment-view.md), and
[runtime flows](docs/architecture/runtime-flows.md).

## Build and verify

Run `scripts/test-linux-runtime.sh` for a reproducible, privileged Linux
run of the full unit + integration test suite (re-execs itself inside a
container automatically on macOS/Windows).

```bash
make test
make docs
make production-gate
```

`make production-gate` is intentionally expensive: it includes race tests,
privileged runtime stress, security and vulnerability checks, fuzzing, chaos,
backup recovery, packaged HA/upgrade tests, performance envelopes, and signed
release verification.

## Documentation map

- [`docs/README.md`](docs/README.md) — reader-oriented documentation index for
  architecture, operations, design, testing, and release qualification.
- [`docs/architecture/README.md`](docs/architecture/README.md) — C4-style
  system context and container views, production deployment topology, and
  dynamic runtime/reconciliation diagrams.
- [`docs/architecture/overview.md`](docs/architecture/overview.md) — design
  principles, identifier model, control-plane state machines, and non-goals.
- [`docs/design/`](docs/design/) — subsystem design docs (runtime, container
  lifecycle, failure model, image store/snapshots, security model, cgroups,
  networking, reconciliation, control plane, and scheduling; more are added as
  their phase begins).
- [`docs/adr/`](docs/adr/) — architecture decision records for frozen
  decisions.
- [`docs/operations/`](docs/operations/) — outcome-oriented installation,
  hardening, availability, recovery, upgrade, and incident runbooks.
- [`docs/contributing/documentation.md`](docs/contributing/documentation.md) —
  Markdown structure, writing rules, diagram conventions, and review checklist.

## Scope

Linux only, cgroup v2 only, with signed amd64 and arm64 release artifacts. See
[docs/architecture/overview.md](docs/architecture/overview.md#7-non-goals) for
explicit non-goals.
