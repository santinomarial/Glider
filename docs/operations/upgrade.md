# Control-plane upgrade and rollback

Glider 0.2 introduces the persisted cluster schema contract. Schema v1 is the
legacy keyspace; schema v2 adds the atomic quota ledger. Version 2 can read v1
and v2, but may write only after migration to v2. Version 1 may read v2 for
inspection but must not write it.

## Upgrade to schema v2

1. Create and verify an encrypted etcd backup.
2. Stop or make read-only every older control-plane process. Older releases do
   not honor the migration mutex or maintain the quota ledger.
3. Run `glider-admin schema migrate` with the production etcd TLS flags,
   cluster ID, and the same quota values used by the service units.
4. Run `glider-admin schema status`; require version 2, minimum reader 1, and
   minimum writer 2.
5. Start one new control-plane canary. Exercise authenticated reads, a
   create/delete cycle, scheduling, metrics, and secret delivery.
6. Roll the remaining replicas. All replicas independently verify the schema
   and persisted quota configuration before opening their API listener.

Migration is serialized by an etcd lease-backed mutex and is crash resumable.
If a process dies after creating the quota ledger but before advancing the
schema marker, the next attempt verifies that ledger and completes the marker.

## Roll back to schema v1

1. Stop every schema-v2 writer. Keep the v2 `glider-admin` binary available.
2. Ensure no task or assignment references secrets; the downgrade refuses to
   proceed otherwise because a v1 node would omit their delivery.
3. Run `glider-admin schema downgrade --target 1` with production etcd TLS
   flags and the cluster ID. This removes the v2 quota ledger and changes the
   marker atomically.
4. Confirm schema version 1, then start the previous control-plane release.
5. If rollback validation fails, stop it, migrate forward again, or restore the
   verified pre-upgrade snapshot according to the disaster-recovery runbook.

Never run v1 and v2 writers concurrently. A mixed read-only observation window
is permitted by the reader compatibility bound; mixed writers are not.

`make upgrade-test` is the release qualification for this procedure. It builds
the signed current release archives, extracts the native packaged binaries,
builds the pinned last pre-schema writer (`4341694`), and starts a mutually
authenticated TLS etcd cluster. The test migrates to v2, performs a create and
delete through the packaged current control plane, stops it, downgrades to v1,
then performs the same canary lifecycle through the legacy binary. Any binary
startup, schema bound, API mutation, or rollback failure fails the gate.
