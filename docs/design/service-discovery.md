# Service discovery

Glider services select tasks by exact-match labels and expose a stable
`<service>.glider` DNS name. The service controller publishes only tasks that
are Ready and have a valid container IP reported by their assigned node. A
readiness failure, restart, or node eviction therefore removes the endpoint on
the next level-triggered reconciliation pass.

```json
{
  "apiVersion": "glider.dev/v1",
  "metadata": {"id": "api", "name": "api"},
  "spec": {"selector": {"app": "api"}, "port": 80, "target_port": 8080}
}
```

Start the control plane with `--dns-listen :53` on the cluster DNS address.
Queries for `api.glider` return A records for its current Ready endpoints. DNS
answers have a short TTL and the endpoint list is deterministically ordered.

Empty selectors match nothing, invalid task addresses are never published,
assignment generation is retained for stale-state audits, status updates use
revision compare-and-swap, and restart or eviction clears the old address.
