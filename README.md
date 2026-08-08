# Glider

Glider is a Linux-native distributed container platform written in Go. It
implements its own container runtime (namespaces, cgroup v2, OverlayFS,
capabilities/seccomp), its own OCI image handling (content-addressed store,
layer unpacker, snapshotter), its own single- and multi-node networking
(veth/bridge/IPAM, VXLAN overlay), and a reconciliation-driven control plane
(etcd-backed desired state, scheduler, node leases, fencing) — rather than
wrapping Docker, containerd, or Kubernetes.

Status: **Phase 2 — container lifecycle, PID 1 supervision, and crash
recovery**, on top of Phase 1's real Linux namespace runtime. See
[docs/architecture/overview.md](docs/architecture/overview.md) for the
end-to-end design and the phase plan, and
[docs/adr/0006-glider-init-pid1-supervisor.md](docs/adr/0006-glider-init-pid1-supervisor.md)
for the current runtime architecture.

## Documentation map

- [`docs/architecture/overview.md`](docs/architecture/overview.md) — system
  architecture, identifier model, control-plane state machines, design
  philosophy.
- [`docs/design/`](docs/design/) — subsystem design docs (runtime, container
  lifecycle, failure model, security model; more are added as their phase
  begins).
- [`docs/adr/`](docs/adr/) — architecture decision records for frozen
  decisions.

## Scope

Linux only, cgroup v2 only, amd64 first. See
[docs/architecture/overview.md](docs/architecture/overview.md#non-goals) for
explicit non-goals.
