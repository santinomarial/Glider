# Storage pressure and workload evacuation

Every node runs reference-safe image garbage collection before evaluating its
configured absolute and percentage free-space reserves. If the reserve remains
violated, `gliderd` first atomically marks its node unschedulable and
`DRAINING`, records total/available bytes and `storage_pressure`, then evicts
all generation-fenced assignments through the control plane. The ordinary
agent reconciliation removes their writable snapshots, logs, cgroups, and
network endpoints; pending tasks may schedule only onto other Ready nodes with
capacity. A durable `StoragePressureEviction` warning event records the action.

This is deliberately a node evacuation policy, not local deletion of arbitrary
files. Glider never removes active workload data while retaining its assignment
and never immediately restarts an evicted task on the pressured node. The node
remains cordoned after free space recovers so an operator can diagnose leaked
logs, image growth, filesystem errors, or incorrect sizing.

After addressing the cause, verify the configured reserve for at least one GC
interval, inspect the warning event and affected tasks, then explicitly restore
the node to Ready/schedulable state through the approved node-management
workflow. Repeated pressure after uncordon requires replacement or capacity
expansion, not a lower safety threshold.

The privileged qualification fills a dedicated 16 MiB tmpfs past the reserve,
proves pressure detection, drives cordon-before-eviction, simulates assignment
data cleanup, and verifies that the real filesystem reserve recovers.
