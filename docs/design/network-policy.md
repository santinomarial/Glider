# Task network policy

Task and workload templates may define stateful IPv4 ingress and egress policy:

```json
{
  "default_deny_ingress": true,
  "default_deny_egress": true,
  "ingress": [{"cidr": "10.64.0.0/16", "protocol": "tcp", "ports": [8080]}],
  "egress": [
    {"cidr": "10.64.0.0/16", "protocol": "tcp", "ports": [8080]},
    {"cidr": "10.64.0.1/32", "protocol": "udp", "ports": [53]}
  ]
}
```

Policy is optional and default-allow for compatibility. Allow rules are valid
only when their direction is default-deny. CIDRs must be canonical IPv4
prefixes; protocols are `tcp`, `udp`, `icmp`, or empty for any protocol. Port
lists are valid only for TCP/UDP. Admission bounds rules and ports before the
policy reaches storage.

The scheduler copies policy into the generation-fenced assignment. `gliderd`
persists it in the endpoint record before programming nftables. Each protected
endpoint gets separate egress and ingress chains: an allowed egress packet must
return from the source chain and then independently pass the destination's
ingress chain. Established/related conntrack traffic is accepted so replies do
not require reverse rules. Published ports and service DNAT are evaluated
before forwarding policy; CIDR rules therefore match the actual selected
backend packet addresses.

The complete Glider nftables table is reconstructed from durable endpoint and
service snapshots after every endpoint/service reconciliation. A privileged
integration test deletes the table, demonstrates loss of enforcement, triggers
level reconciliation, and proves default-deny is restored. Alert on unexpected
table deletion because enforcement is unavailable until the next resync.

Operators must explicitly allow cluster DNS, health-probe sources, required
service backend CIDRs/ports, and external dependencies. IPv6 is outside the
current deployment envelope and is rejected at admission rather than silently
bypassing policy.
