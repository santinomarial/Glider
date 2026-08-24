# Glider documentation

Glider is a Linux-native distributed container platform. This index routes
readers by intent: understand the system, operate a cluster, look up a
contract, or verify a release.

> [!IMPORTANT]
> Glider's software release gate passes locally, but production certification
> remains specific to a deployment. Review the
> [production readiness gates](release/production-readiness.md) before placing
> critical workloads on a cluster.

## Start here

| If you want to… | Start with | Then read |
|---|---|---|
| Understand what Glider does | [Architecture overview](architecture/overview.md) | [System context](architecture/system-context.md) and [container view](architecture/container-view.md) |
| Install a node | [Installation](operations/install.md) | [Hardening](operations/hardening.md) and [PKI](operations/pki.md) |
| Operate a cluster | [High availability](operations/high-availability.md) | [Monitoring](operations/monitoring.md), [backup/restore](operations/backup-restore.md), and [incident response](operations/incident-response.md) |
| Understand one subsystem | [Design documents](#design-and-reference) | The related [architecture decisions](#architecture-decisions) |
| Qualify a release | [Production gate](testing/production-gate.md) | [Environment evidence](release/environment-evidence.md) and [readiness gates](release/production-readiness.md) |
| Contribute documentation | [Documentation standard](contributing/documentation.md) | [Architecture diagram standard](architecture/README.md) |

## Architecture

The architecture set uses progressively deeper views. Start broad and zoom in
only as far as the decision or incident requires.

1. [Architecture map and notation](architecture/README.md)
2. [System context](architecture/system-context.md) — users, Glider, and
   external dependencies
3. [Container view](architecture/container-view.md) — deployable processes,
   data stores, protocols, and ownership
4. [Deployment view](architecture/deployment-view.md) — production topology,
   trust zones, and failure domains
5. [Runtime and reconciliation flows](architecture/runtime-flows.md) — dynamic
   request, scheduling, execution, and fencing sequences
6. [Architecture overview](architecture/overview.md) — principles, identifiers,
   state machines, and non-goals

## Operations

- [Installation and host integration](operations/install.md)
- [Production hardening](operations/hardening.md)
- [Capacity and sizing](operations/sizing.md)
- [High availability](operations/high-availability.md)
- [Monitoring and alert response](operations/monitoring.md)
- [Backup and restore](operations/backup-restore.md)
- [Upgrade and rollback](operations/upgrade.md)
- [PKI and certificate rotation](operations/pki.md)
- [Node join, drain, removal, and replacement](operations/nodes.md)
- [Storage pressure](operations/storage-pressure.md)
- [Incident response](operations/incident-response.md)
- [Cluster decommission](operations/decommission.md)

## Design and reference

| Domain | Authoritative documents |
|---|---|
| Runtime | [Runtime](design/runtime.md), [container lifecycle](design/container-lifecycle.md), [cgroup v2](design/cgroups.md), [security model](design/security-model.md) |
| Images | [OCI image store and OverlayFS snapshots](design/image-store.md) |
| Control plane | [Networking and control plane](design/networking-control-plane.md), [leases and reconciliation](design/leases-overlay-workloads-health.md), [admission](design/admission.md), [cluster quotas](design/cluster-quotas.md) |
| Networking | [Network policy](design/network-policy.md), [service discovery](design/service-discovery.md) |
| Workloads | [Rolling deployments](design/rolling-deployments.md), [secrets](design/secrets.md), [node operations](design/node-operations.md) |
| Security | [Control-plane security](design/control-plane-security.md), [security model](design/security-model.md), [failure model](design/failure-model.md) |
| Interfaces | [API versioning](design/api-versioning.md), [CLI](design/cli.md), [observability](design/observability.md) |

## Architecture decisions

Architecture decision records are immutable decision history. Supersede an ADR
with a new ADR; do not silently rewrite an accepted decision.

- [ADR 0001: Linux and cgroup v2 only](adr/0001-linux-cgroup-v2-only.md)
- [ADR 0002: OCI image model](adr/0002-oci-image-model.md)
- [ADR 0003: etcd control-plane state](adr/0003-etcd-control-plane.md)
- [ADR 0004: OverlayFS snapshotter](adr/0004-overlayfs-snapshotter.md)
- [ADR 0005: reconciliation-driven node agent](adr/0005-reconciliation-driven-node-agent.md)
- [ADR 0006: Glider init as PID 1](adr/0006-glider-init-pid1-supervisor.md)

## Testing and release

- [Production release gate](testing/production-gate.md)
- [Security gate](testing/security.md)
- [Chaos qualification](testing/chaos.md)
- [Performance qualification](testing/performance.md)
- [Compatibility matrix](release/compatibility-matrix.md)
- [Release-environment evidence](release/environment-evidence.md)
- [Production readiness](release/production-readiness.md)
- [v0.1.0 release audit](release/v0.1.0.md)

## Documentation contract

Each page has one primary job. Procedures use ordered, verifiable steps;
reference pages state exact contracts; explanations make trade-offs explicit;
and architecture diagrams name ownership, protocols, trust boundaries, and
failure domains. See the [documentation standard](contributing/documentation.md).
