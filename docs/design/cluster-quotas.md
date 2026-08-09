# Cluster quota invariants

Glider persists one versioned quota ledger per cluster under the authoritative
etcd keyspace. Every task create, delete, or resource-size change updates that
ledger in the same transaction as the resource mutation. Workload and service
counts use the same boundary. Concurrent API replicas therefore cannot both
observe spare quota and over-admit it.

Control-plane replicas must start with identical positive quota flags. The
first replica bootstraps usage from existing resources before serving the API;
later replicas compare their configuration to the persisted limits and refuse
startup on mismatch. Operators must quiesce older releases that do not maintain
the ledger before enabling quota during an upgrade.

Status-only updates avoid writing the ledger because their quota footprint is
unchanged. Revision comparisons still protect the resource itself. A quota CAS
conflict is safe to retry from fresh state; an exceeded limit is returned as
`ResourceExhausted`.
