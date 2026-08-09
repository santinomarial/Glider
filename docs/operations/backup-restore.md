# Backup and restore

`glider-admin backup` requests a point-in-time snapshot from exactly one
mutually authenticated etcd member. It writes with create-exclusive semantics,
encrypts with AES-256-CTR, authenticates the complete envelope with
HMAC-SHA-256 using an independent key half, fsyncs, and never leaves a partial
destination after failure. The 64-byte key file must have owner-only mode.

`glider-admin verify` authenticates and decrypts into a private temporary file,
then asks etcd's snapshot library to validate the database and print its
revision, hash, key count, and size. `glider-admin restore` repeats verification
and refuses an existing output directory. It restores a new cluster identity,
bumps the revision, and marks prior revisions compacted so watch clients cannot
silently continue from a revision that moved backward.

Backups must be copied to independent immutable storage and the key must be
held in a separate secret manager. An encrypted file without its key is not a
recoverable backup. Operators must regularly restore into an isolated cluster
and verify Glider resources before accepting the recovery point.

Generate the key once with `glider-admin backup-key
--output=/etc/glider/backup.key`, escrow it outside the cluster, configure
`/etc/glider/backup.env`, and enable `glider-backup.timer`. The hourly job uses
create-exclusive filenames, verifies every new backup before success, and
deletes local recovery points older than the configured retention period.
Replication to immutable off-host storage remains an operator responsibility.

## Recovery-point exercise

At least monthly and before every upgrade:

1. Copy one verified encrypted backup and its separately escrowed key into an
   isolated recovery environment.
2. Run `glider-admin verify`; record its hash, revision, key count, size, source
   cluster, and creation time in the recovery log.
3. Stop the isolated etcd member and restore to an empty directory with a new
   cluster token and the default revision bump.
4. Start etcd from the restored directory, then start one control-plane replica
   with outbound workload scheduling disabled or with all recovered nodes
   cordoned.
5. Confirm schema status, quotas, workloads, services, assignments, secrets,
   and recent audit events. Secret payloads must remain redacted through APIs.
6. Destroy the isolated plaintext snapshot and restored data after recording
   the result. A failed decrypt, snapshot validation, or resource check rejects
   the recovery point and pages the on-call operator.
