# Monitoring and alert response

Scrape every control-plane replica's mTLS metrics endpoint, not only the load
balancer. Configure Prometheus with a client certificate carrying an approved
operator role and the Glider CA. Load `glider.rules.yml` from the release
archive and route `critical` alerts to the on-call pager.

Import the packaged `glider-dashboard.json` into Grafana. It shows request rate,
non-`OK` rate, p99 request latency, in-flight requests, leader ownership,
metrics snapshot failures, node phases, and task readiness. Keep method and code
labels; do not aggregate on request ID, principal, task, or node in
long-retention metrics backends unless cardinality limits are enforced.

Every authenticated gRPC completion emits one JSON line to stderr with UTC
time, component, request ID, principal, full method, status code, and duration.
Both long-running daemons also emit JSON lifecycle, warning, and failure logs.
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

Retain daemon JSON logs and audit events for at least the incident-review
period. Run `make monitoring` for every alert-rule change; it validates the
rules and proves representative critical alerts cross their hold periods. A
release-environment fire drill must additionally verify scraping, routing, and
on-call delivery.
