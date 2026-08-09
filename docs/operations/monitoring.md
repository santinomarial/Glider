# Monitoring and alert response

Scrape every control-plane replica's mTLS metrics endpoint, not only the load
balancer. Configure Prometheus with a client certificate carrying an approved
operator role and the Glider CA. Load `glider.rules.yml` from the release
archive and route `critical` alerts to the on-call pager.

The primary API dashboard should show request rate by method, non-`OK` rate by
code, p50/p95/p99 request latency, in-flight requests, leader ownership and
changes, metrics snapshot failures, node phases, workload/service totals, and
task readiness. Keep method and code labels; do not aggregate on request ID,
principal, task, or node in long-retention metrics backends unless cardinality
limits are enforced.

Every authenticated gRPC completion emits one JSON line to stderr with UTC
time, component, request ID, principal, full method, status code, and duration.
The same request ID is returned in the `x-request-id` response header. Preserve
it through ingress proxies and use it to correlate client failures, service
logs, and durable audit events. Treat principals and object identifiers as
sensitive operational metadata.

## Alerts

- `GliderNoControllerLeader`: inspect etcd quorum and replica lease errors. Do
  not restart all replicas together.
- `GliderControllerLeadershipChurn`: check network loss, clock behavior, etcd
  latency, and rolling-update sequencing.
- `GliderAPIErrorRateHigh` or `GliderAPIP99LatencyHigh`: split by method/code,
  inspect in-flight saturation and etcd health, then correlate request IDs.
- `GliderMetricsSnapshotFailed`: authoritative state could not be read; verify
  etcd before trusting older dashboard values.
- `GliderNodeUnreachable`: confirm lease loss, cordon/drain if connectivity is
  not restored, and follow node replacement procedures.

Retain control-plane JSON logs and audit events for at least the incident-review
period. Alert-rule changes require a syntax check and a staging fire drill.
