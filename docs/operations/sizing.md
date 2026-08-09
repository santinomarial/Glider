# Capacity and sizing

The qualified control-plane baseline is three replicas with 2 CPU cores and
2 GiB memory each, plus three etcd members with low-latency durable SSDs. Start
worker nodes with 2 GiB reserved for the OS, Glider, image unpacking, and log
bursts; schedule only the remaining capacity.

The release regression envelope requires scheduling across 1,000 candidate
nodes in at most 500 microseconds with at most 600 KiB allocated, and resolving
among 1,000 service endpoints in at most 250 nanoseconds with at most 128 bytes
allocated. `make benchmark` fails when either bound is exceeded. The bounds are
per-operation algorithmic guards, not end-user request-latency promises.

Keep etcd database usage below 70%, node disks below the configured pressure
threshold, API p99 below one second, and non-OK request rate below 5%. Add
capacity before sustained CPU exceeds 70% or in-flight requests trend upward.
Load-test the actual release environment when exceeding 1,000 nodes, changing
hardware, or changing the underlay and record the new envelope.
