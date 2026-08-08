# Glider

Glider is a Linux-native distributed container platform written in Go. It
implements its own container runtime (namespaces, cgroup v2, OverlayFS,
capabilities/seccomp), its own OCI image handling (content-addressed store,
layer unpacker, snapshotter), its own single- and multi-node networking
(veth/bridge/IPAM, VXLAN overlay), and a reconciliation-driven control plane
(etcd-backed desired state, scheduler, node leases, fencing) — rather than
wrapping Docker, containerd, or Kubernetes.

Status: **Phase 8 — secured OCI image execution**. Glider now resolves OCI
references, verifies and atomically stores registry content, safely unpacks
immutable layers, mounts per-container OverlayFS snapshots, and applies a
reduced capability set, `no_new_privs`, and seccomp before workload
execution. This builds on Phase 4's cgroup v2 limits, Phase 2's PID 1
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
and [docs/design/security-model.md](docs/design/security-model.md) for Phase 8.

Run `scripts/test-linux-runtime.sh` for a reproducible, privileged Linux
run of the full unit + integration test suite (re-execs itself inside a
container automatically on macOS/Windows).

## Documentation map

- [`docs/architecture/overview.md`](docs/architecture/overview.md) — system
  architecture, identifier model, control-plane state machines, design
  philosophy.
- [`docs/design/`](docs/design/) — subsystem design docs (runtime, container
  lifecycle, failure model, image store/snapshots, security model, cgroups; more are added as
  their phase begins).
- [`docs/adr/`](docs/adr/) — architecture decision records for frozen
  decisions.

## Scope

Linux only, cgroup v2 only, amd64 first. See
[docs/architecture/overview.md](docs/architecture/overview.md#non-goals) for
explicit non-goals.
