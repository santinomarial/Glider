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

Assignment-fenced delivery to nodes is a separate boundary: a node may receive
only values referenced by an assignment currently owned by that node. Until
that delivery path is configured, stored secrets cannot be attached to tasks.
