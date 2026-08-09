# Production readiness gates

Glider v0.1.0 completes the educational master-plan milestone, but it is not a
production release. A production tag may be created only when every **P0** row
below is implemented, documented, and exercised by an automated release gate.
Passing unit tests alone does not close a row.

| Area | Priority | v0.1.0 evidence | Production exit gate |
|---|---:|---|---|
| API identity and encryption | P0 | CLI uses plaintext gRPC | Mutual TLS, certificate rotation, authenticated principals, and negative tests |
| Authorization | P0 | No policy enforcement | Deny-by-default RBAC for mutations, exec, logs, stats, node registration, and events |
| Node operations | P0 | `logs`, `exec`, `stats` fail explicitly | Bounded authenticated streaming, generation fencing, terminal resize/cancel, and audit records |
| API admission | P0 | Central validation, transport limits, rate limits, atomic quotas, durable mutation idempotency keys, and admission fuzz target exist | Sustained release-candidate fuzz evidence |
| Secret handling | P0 | Encrypted objects, redaction, node/generation-fenced delivery, ephemeral injection, audited access, rotation/reassignment mTLS, and persisted wrong-key/corrupt-ciphertext fail-closed recovery exercises exist | Release-environment secret-manager loss and recovery drill |
| Control-plane HA | P0 | Multi-replica APIs, lease-elected authority, three-member etcd Raft-leader loss, and packaged API/leader-process failover qualifications exist | Release-environment multi-host load-balancer exercise |
| Backup and disaster recovery | P0 | Encrypted authenticated snapshots, hourly systemd automation, verified revision-bumped restore, recovery exercise, and corrupt/wrong-key rejection exist | Scheduled off-host immutable-copy and monthly restore evidence from the release environment |
| Upgrades and schema migration | P0 | Compatibility bounds, mutexed crash-resumable migration, guarded rollback, concurrency tests, and packaged current/legacy TLS-etcd canary qualification exist | Release-environment rolling replica exercise |
| Node lifecycle | P0 | Identity-bound join, lease fencing, cordon/drain, safe removal, replacement runbook, and last-good hot activation of externally renewed leaf certificates exist | Certificate-manager renewal and full node replacement qualification |
| Storage and disk pressure | P0 | Image GC, admission thresholds, cordon-before-evict node policy, durable pressure/event status, and real constrained-filesystem recovery test exist | Release-environment sustained disk-pressure qualification |
| Network policy and exposure | P0 | Overlay/NAT, stable service VIP/DNS/load balancing, stateful ingress/egress policy, deletion recovery, underlay-aware MTU validation, and real two-node VXLAN/service packet qualification exist | Release-environment multi-host qualification |
| Observability | P0 | Resource metrics, durable bounded events, correlated JSON daemon/API logs, request latency/error/saturation histograms, leadership/snapshot signals, a packaged Grafana dashboard, and executable alert fire-drill tests exist | Release-environment scrape, notification-routing, and on-call delivery fire drill |
| Packaging and host integration | P0 | Reproducible signed static binaries, hardened units, sysusers/tmpfiles, installer/uninstaller tests, non-executing fail-closed config/TLS preflight, and packaged canary upgrade qualification exist | Release-environment host acceptance |
| Release security | P0 | Isolation gate, pinned official reachable-code vulnerability scan, response SLAs, SPDX 2.3 SBOM, in-toto SLSA v1 provenance, checksums, signatures, and tamper-negative verification exist | Independent threat-model review |
| Reliability qualification | P0 | Unit/race/runtime, real kernel-network and disk-pressure tests, lease-partition self-fencing SLO, packaged controller crash storm, packaged control-plane loss, three-member etcd leader loss, encrypted recovery, and a consolidated evidence-producing gate exist | Release-environment multi-host soak and fault qualification |
| Performance qualification | P1 | Published baseline sizing, saturation signals, p99 operational SLO, mean/allocation guards, exact-commit throughput plus p50/p95/p99 reports for simulated 100/1,000-node scheduling and 1,000-endpoint discovery, and real-kernel single-host warm-rootfs full-lifecycle percentiles exist | Cold image pull/unpack and release-hardware multi-host load/saturation qualification |
| Operator documentation | P1 | Installation, hardening, sizing, monitoring, backup/restore, upgrade/rollback, HA, PKI, node, storage-pressure, incident, and decommission runbooks exist | Release-environment review and rehearsal |

## Release rule

`make production-gate` generates an evidence bundle containing
the exact source commit, test outputs, benchmark results, SBOM, signatures,
compatibility matrix, migration result, and disaster-recovery result. The
workflow fails closed if a P0 gate is missing, skipped, flaky, or relies only
on a manual assertion.

`scripts/qualify-environment.sh` executes the canonical multi-host assertion
matrix. `scripts/production-release.sh` then requires that output and the
independent review to be signed under the evidence contract in
`environment-evidence.md`. A local operator cannot self-assert or bypass these
external P0 results.

## Scope boundary

“Production complete” means safe and supportable for Glider's documented
Linux/cgroup-v2 deployment envelope. It does not mean feature parity with
Kubernetes, support for arbitrary kernels, or immunity from kernel-level
container escapes. Those remain explicit non-goals or external dependencies.
