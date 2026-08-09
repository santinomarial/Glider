# Service discovery

Glider services select tasks by exact-match labels, receive a stable virtual IP
from `10.96.0.0/16`, and expose a stable `<service>.glider` DNS name. The
service controller publishes only tasks that
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
Queries for `api.glider` return the stable virtual IP while at least one Ready
endpoint exists. Every node durably records the complete service snapshot and
rebuilds Glider's nftables table level-triggeredly. New TCP connections are
distributed uniformly across local or VXLAN-routed Ready endpoints; conntrack
keeps each established flow pinned. Empty services reject traffic rather than
forwarding to stale tasks.

Address allocation resolves collisions deterministically under the single
controller lease. A privileged two-node qualification sends a service-VIP TCP
flow through node-local DNAT and the VXLAN overlay to a backend in a remote pod
CIDR. Empty selectors match nothing, invalid task addresses are never published,
assignment generation is retained for stale-state audits, status updates use
revision compare-and-swap, and restart or eviction clears the old address.
