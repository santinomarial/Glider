# API admission

All externally mutable resources pass centralized validation before etcd.
Admission bounds identifiers, labels, images, command count and aggregate
size, CPU and memory, host ports, probes, replica counts, rollout budgets,
service selectors/ports, and event payloads. Empty selectors and duplicate
host ports fail closed. Invalid requests return gRPC `InvalidArgument` and
cannot consume revisions or controller work.

The gRPC server additionally caps requests at 1 MiB, responses at 4 MiB, and
concurrent HTTP/2 streams at 256. These are safety ceilings, not tenant quotas.
Per-principal rate limits, cluster quotas, idempotency keys, secret admission,
and delete/drain policy remain open production gates.
