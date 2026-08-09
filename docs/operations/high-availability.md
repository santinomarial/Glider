# Control-plane high availability

Run at least three etcd members in separate failure domains and two or more
`glider-controlplane` replicas behind a TCP load balancer. Every replica serves
the stateless authenticated API, metrics, and optional DNS endpoint. Give each
replica a unique stable `--instance-id`.

Mutating reconciliation loops use one etcd lease-backed election per cluster.
Only the elected replica runs workload, service, scheduler, node-monitor, and
event-retention loops. Loss of the election lease cancels all loops before the
replica campaigns again. Every individual write remains revision/CAS guarded,
because an operation already in flight may straddle lease loss.

API availability does not depend on controller leadership: followers continue
serving reads and transactional user mutations. Controller convergence pauses
during election transfer and resumes from etcd desired state on the successor.
Alert if no election key exists for longer than two lease TTLs or if leadership
changes repeatedly without an operator action or etcd incident.

Replicas must use identical schema, quota, PKI trust, and secret-encryption key
configuration. A mismatch fails startup rather than creating split policy.
