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
