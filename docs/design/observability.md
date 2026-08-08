# Observability

The control plane exposes a Prometheus text endpoint (default
`127.0.0.1:9090`) with node phase counts, per-task readiness, and workload and
service totals. Task series carry stable `task_id`, `workload_id`, and
`node_id` labels so an operator can correlate a symptom with durable state.

Structured `Event` resources provide an ordered audit stream with type,
reason, object kind/ID, node ID, timestamp, message, and arbitrary structured
fields. Events are stored under the cluster's etcd prefix and are available
through `PutEvent` and `ListEvents`. Metrics snapshots fail closed with HTTP
503 when authoritative state cannot be read rather than publishing partial
values as truth.
