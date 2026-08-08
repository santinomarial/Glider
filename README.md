# Glider

Glider is a Linux-native distributed container platform written in Go. It
implements its own container runtime (namespaces, cgroup v2, OverlayFS,
capabilities/seccomp), its own OCI image handling (content-addressed store,
layer unpacker, snapshotter), its own single- and multi-node networking
(veth/bridge/IPAM, VXLAN overlay), and a reconciliation-driven control plane
(etcd-backed desired state, scheduler, node leases, fencing) — rather than
wrapping Docker, containerd, or Kubernetes.

Status: **v0.1.0 master-plan milestone complete; production hardening in progress**. Glider now
adds bridge/veth networking, persistent IPAM, DNS, NAT and host-port
publication; a restart-safe `gliderd` reconciliation loop; an etcd-backed
versioned gRPC control plane; and a resource-aware scheduler whose assignment
and reservation commit atomically; renewable node leases and self-fencing;
VXLAN node-subnet networking; replica reconciliation and node-failure
replacement; and distinct startup, liveness, and readiness semantics with
bounded restart backoff. Workload template revisions roll forward under durable
`maxSurge`/`maxUnavailable` budgets, and stalled replacements preserve healthy
old capacity. Services select labeled tasks, publish only Ready container
addresses, and resolve through stable `<service>.glider` DNS names. This builds on Phase 8's secured OCI image
execution, Phase 4's cgroup v2 limits, and Phase 2's PID 1
supervision/crash recovery, and Phase 1's Linux namespace runtime. (Phase 3's originally
planned mount-isolation/`pivot_root` scope was completed early, inside
Phase 1's exit contract — see [docs/design/cgroups.md](docs/design/cgroups.md)'s
"Note on phase numbering".) See
[docs/architecture/overview.md](docs/architecture/overview.md) for the
end-to-end design and the phase plan,
[docs/adr/0006-glider-init-pid1-supervisor.md](docs/adr/0006-glider-init-pid1-supervisor.md)
for the runtime's process architecture, and
[docs/design/cgroups.md](docs/design/cgroups.md) for resource isolation,
[docs/design/image-store.md](docs/design/image-store.md) for Phases 5–7,
and [docs/design/security-model.md](docs/design/security-model.md) for Phase 8,
and [docs/design/networking-control-plane.md](docs/design/networking-control-plane.md)
for Phases 9–12.
See [docs/design/leases-overlay-workloads-health.md](docs/design/leases-overlay-workloads-health.md)
for Phases 13–16.
See [docs/design/rolling-deployments.md](docs/design/rolling-deployments.md) for
Phase 17.
See [docs/design/service-discovery.md](docs/design/service-discovery.md) for
Phase 18.
See [docs/design/observability.md](docs/design/observability.md) for Phase 19.
See [docs/design/cli.md](docs/design/cli.md) for Phase 20.
See [docs/testing/chaos.md](docs/testing/chaos.md) for Phase 21.
See [docs/testing/security.md](docs/testing/security.md) for Phase 22.
See [docs/testing/performance.md](docs/testing/performance.md) for Phase 23.
See [docs/release/v0.1.0.md](docs/release/v0.1.0.md) for Phase 24 and release gates.
Production release blockers and their objective exit gates are tracked in
[docs/release/production-readiness.md](docs/release/production-readiness.md).
The first hardening milestone adds mandatory control-plane mTLS and
certificate-role authorization; see
[docs/design/control-plane-security.md](docs/design/control-plane-security.md).

Run `scripts/test-linux-runtime.sh` for a reproducible, privileged Linux
run of the full unit + integration test suite (re-execs itself inside a
container automatically on macOS/Windows).

## Documentation map

- [`docs/architecture/overview.md`](docs/architecture/overview.md) — system
  architecture, identifier model, control-plane state machines, design
  philosophy.
- [`docs/design/`](docs/design/) — subsystem design docs (runtime, container
  lifecycle, failure model, image store/snapshots, security model, cgroups,
  networking, reconciliation, control plane, and scheduling; more are added as
  their phase begins).
- [`docs/adr/`](docs/adr/) — architecture decision records for frozen
  decisions.

## Scope

Linux only, cgroup v2 only, amd64 first. See
[docs/architecture/overview.md](docs/architecture/overview.md#non-goals) for
explicit non-goals.
