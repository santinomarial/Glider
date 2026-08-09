# Secret storage and access

Secret values are never persisted as plaintext. The control plane encrypts the
JSON data map with AES-256-GCM before calling the etcd store. The cluster ID and
secret ID are authenticated as associated data, preventing ciphertext from
being moved between clusters or names. A keyed payload MAC supports safe
idempotent retries without disclosing or decrypting data during conflict
resolution.

The 32-byte master key is loaded from a root-controlled mode-0600 file. Create
one with:

```bash
glider-admin secret-key --output /etc/glider/pki/secret.key
```

`ListSecrets` returns metadata only. There is intentionally no operator API to
read secret values back. Admin identities may create, rotate, and delete secret
objects; operator identities may list redacted metadata. Secret values should
be supplied from files so they do not appear in shell history:

```bash
glider secret put database password=/secure/input/password
```

Tasks reference individual secret keys and map them to environment names. A
node fetches values over mTLS only for an assignment whose node ID and current
generation exactly match its certificate identity. Every successful delivery
is durably audited before the response is returned. Secret environment is
passed to the workload but excluded from node-local runtime state.

Rotating a secret replaces its encrypted object with a revision-guarded write.
Existing processes retain their original environment; after reassignment or a
rolling restart, the new assignment generation receives the rotated value and
the superseded generation is denied.
