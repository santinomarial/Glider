# Node join, drain, removal, and replacement

A node joins with a client certificate whose role is `node`, common name is the
stable node ID, and SPIFFE URI belongs to the target cluster. The control plane
allows that identity to create or update only the node object with the same ID;
it cannot impersonate a peer. Issue the certificate through the cluster's
approved certificate manager, configure identical node ID values in the
certificate and `gliderd`, then confirm the node lease and `READY` status.

To remove or replace a node:

1. Run `glider drain NODE` and wait until its assignments are rescheduled and
   reserved CPU/memory reach zero.
2. Stop and disable `gliderd` on that host. Confirm its etcd lease has expired.
3. Run `glider remove-node NODE`. The API compares the exact node revision and
   refuses a schedulable node, a non-drained phase, reservations, assignments,
   or any live node lease.
4. Revoke or expire the node certificate in the external certificate manager,
   wipe `/var/lib/glider` before repurposing the host, and remove stale network
   routes only after peers have converged.
5. Join the replacement with a new stable node ID and certificate. Reusing an
   old ID is prohibited unless the old host and credential are cryptographically
   destroyed and the removal audit record has been reviewed.

If removal returns `FailedPrecondition`, do not delete etcd keys manually. Find
the surviving lease, assignment, reservation, or daemon and repeat the drain.
