# API admission

All externally mutable resources pass centralized validation before etcd.
Admission bounds identifiers, labels, images, command count and aggregate
size, CPU and memory, host ports, probes, replica counts, rollout budgets,
service selectors/ports, and event payloads. Empty selectors and duplicate
host ports fail closed. Invalid requests return gRPC `InvalidArgument` and
cannot consume revisions or controller work.

Task runtime status is never client-authoritative. Creates accept only an
empty or `PENDING` status and normalize it server-side. Updates preserve the
stored status and generation, reject attempts to forge node ownership,
readiness, restart deadlines, or terminal results, and refuse mutation of
active or workload-controller-owned tasks.

The gRPC server additionally caps requests at 1 MiB, responses at 4 MiB, and
concurrent HTTP/2 streams at 256. These are safety ceilings, not tenant quotas;
per-principal rate limits, atomic cluster quotas, durable idempotency keys,
secret admission, and revision-safe delete/drain policy provide the layered
resource and mutation controls.
