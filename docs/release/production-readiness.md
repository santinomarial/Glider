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
| Secret handling | P0 | Encrypted objects, redaction, node/generation-fenced delivery, ephemeral injection, audited access, and rotation/reassignment mTLS test exist | Release-candidate key-loss and corrupt-ciphertext recovery exercise |
| Control-plane HA | P0 | Multi-replica APIs, lease-elected controller authority, leader-loss transfer, and real three-member etcd Raft-leader-loss qualification exist | Packaged multi-process API failover qualification |
| Backup and disaster recovery | P0 | Encrypted authenticated snapshots, hourly systemd automation, verified revision-bumped restore, recovery exercise, and corrupt/wrong-key rejection exist | Scheduled off-host immutable-copy and monthly restore evidence from the release environment |
| Upgrades and schema migration | P0 | Persisted compatibility bounds, mutexed crash-resumable v1→v2 migration, guarded v2→v1 rollback, and concurrent migration test exist | Packaged-binary canary upgrade and rollback qualification |
| Node lifecycle | P0 | Identity-bound join, lease fencing, cordon/drain, safe removal, replacement runbook, and last-good hot activation of externally renewed leaf certificates exist | Certificate-manager renewal and full node replacement qualification |
| Storage and disk pressure | P0 | Reference-safe image GC, periodic reclamation, and launch admission thresholds implemented | Workload eviction policy and real disk-exhaustion recovery qualification |
| Network policy and exposure | P0 | Overlay/NAT, stable service VIP/DNS/load balancing, generation-fenced stateful ingress/egress policy, deletion recovery, and real namespace packet tests exist | MTU validation and real multi-node overlay/service qualification |
| Observability | P0 | Resource metrics, durable bounded events, correlated JSON API logs, request latency/error/saturation histograms, leadership/snapshot signals, and packaged alert rules exist | Node/runtime structured logs, published dashboard, and staging alert fire drill |
| Packaging and host integration | P0 | Reproducible signed static binaries, hardened systemd units, sysusers/tmpfiles, fixed-mode installer, data-preserving uninstaller, and isolated package acceptance tests exist | Config preflight and packaged canary upgrade qualification |
| Release security | P0 | Isolation gate, pinned official reachable-code vulnerability scan, response SLAs, SPDX 2.3 SBOM, in-toto SLSA v1 provenance, checksums, signatures, and tamper-negative verification exist | Independent threat-model review |
| Reliability qualification | P0 | Unit/race/runtime and limited chaos gates | Real multi-node soak, partitions, control-plane loss, etcd member loss, disk pressure, crash storm, and recovery SLO gates |
| Performance qualification | P1 | Microbenchmarks only | Published target hardware, capacity envelope, saturation behavior, p95/p99 SLOs, and regression thresholds |
| Operator documentation | P1 | Design documents exist | Installation, hardening, sizing, monitoring, backup, restore, upgrade, rollback, incident, and decommission runbooks |

## Release rule

The production release workflow must generate an evidence bundle containing
the exact source commit, test outputs, benchmark results, SBOM, signatures,
compatibility matrix, migration result, and disaster-recovery result. The
workflow fails closed if a P0 gate is missing, skipped, flaky, or relies only
on a manual assertion.

## Scope boundary

“Production complete” means safe and supportable for Glider's documented
Linux/cgroup-v2 deployment envelope. It does not mean feature parity with
Kubernetes, support for arbitrary kernels, or immunity from kernel-level
container escapes. Those remain explicit non-goals or external dependencies.
