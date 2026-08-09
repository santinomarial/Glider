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
| API admission | P0 | Resource validation is distributed and incomplete | Central validation, request/body limits, quotas, rate limits, idempotency, and fuzz tests |
| Secret handling | P0 | No workload secret model | Encrypted-at-rest secret objects, redaction, least-privilege delivery, and rotation |
| Control-plane HA | P0 | One process is demonstrated | Three-member etcd and multiple API/controller instances pass leader/failover tests without duplicate authority |
| Backup and disaster recovery | P0 | None | Automated etcd snapshot, verified restore, recovery-point/runbook test, and corrupt-backup rejection |
| Upgrades and schema migration | P0 | No compatibility or migration machinery | Version-skew policy, forward/backward migration, canary upgrade, and rollback test |
| Node lifecycle | P0 | Lease fencing exists; join/drain UX incomplete | Authenticated join, cordon/drain, safe removal, certificate renewal, and replacement tests |
| Storage and disk pressure | P0 | Reference-safe image GC, periodic reclamation, and launch admission thresholds implemented | Workload eviction policy and real disk-exhaustion recovery qualification |
| Network policy and exposure | P0 | Overlay/NAT/service DNS exist | Ingress/egress policy, service data plane, firewall persistence, MTU validation, and multi-node tests |
| Observability | P0 | Basic metrics and durable event objects | Structured process logs, bounded event retention, latency/error/saturation metrics, alerts, dashboards, and trace correlation |
| Packaging and host integration | P0 | Source-run scripts only | Reproducible signed binaries/packages, systemd units, dedicated users/directories, config validation, install/uninstall/upgrade tests |
| Release security | P0 | Isolation gate exists | Dependency/SBOM scan, provenance, artifact signatures, vulnerability response policy, and independent threat-model review |
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
