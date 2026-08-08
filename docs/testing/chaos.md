# Chaos testing

Run `scripts/test-chaos.sh` to repeatedly inject the concurrency and lifecycle
failures covered by Glider's safety contract. The harness exercises concurrent
bind races, stale assignment generations, node eviction and rescheduling,
atomic reservation release, duplicate controller reconciliation, and agent
restart recovery under the race detector. Set `GLIDER_CHAOS_ITERATIONS` to
increase the default 25 repetitions.

The pass condition is invariant-based: one bind winner, monotonically
increasing generations after eviction, no leaked capacity, desired replica
convergence, and no data races. The harness is deterministic enough for CI but
repeated scheduling and embedded-etcd timing vary the interleavings.
