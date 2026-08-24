# Public API versioning and compatibility

Glider evolves public RPCs by adding a new versioned protobuf package, never by
silently changing an existing wire contract. `glider.v1` remains available
during the v2 migration; `glider.v2` is the typed successor.

## Contract rules

- Every RPC boundary uses a Glider-owned request and response message.
- Field numbers are permanent. Removed fields and enum values are reserved,
  never reused.
- Existing field types, cardinality, JSON names, RPC names, and service names
  do not change within a major API version.
- Additive fields default safely when read by an older client.
- Mutations carry resource metadata with an idempotency key and optimistic
  revision where applicable.
- List requests reserve pagination and filtering fields before unbounded
  resource growth makes them mandatory.
- Secret mutation and list responses never return secret bytes. Decrypted
  values flow only through assignment- and generation-fenced delivery.
- `google.protobuf.Any`, `google.protobuf.Value`, and
  `google.protobuf.Struct` are forbidden. Diagnostic event attributes are a
  typed `map<string, string>` and must remain non-secret.

## Version lifecycle

| Stage | Server behavior | Client guidance |
|---|---|---|
| Draft | Source schema and offline policy checks exist; generated clients or server support may be incomplete | Design review only |
| Candidate | Schema exists and generated clients compile; not yet advertised as default | Do not depend on it outside compatibility testing |
| Current | Served by every supported control-plane replica and used by the bundled CLI/agent | Preferred API |
| Deprecated | Served for at least one documented compatibility window with warnings and metrics | Migrate before the stated removal release |
| Removed | Available only in releases outside the supported compatibility range | Upgrade clients before the server |

The typed v2 contract is **Candidate**. Every control-plane replica serves it
alongside the legacy Struct-based v1 service, and both versions share the same
admission, authorization, idempotency, scheduling, and transactional storage
paths. Pinned generation, immutable-baseline breaking detection,
reproducibility, generated-client compilation, adapter integration tests, and
cross-version RBAC checks pass. The node agent uses v2 for generation-fenced
secret delivery. The contract becomes Current only after the bundled CLI
migrates and mixed-version deployment tests pass together. The legacy v1 API
must not be removed in the same release that first serves v2.

## Generation and policy checks

`scripts/test-api-contract.sh` performs offline repository policy checks.
`scripts/generate-api.sh` uses pinned Buf and Go/gRPC plugin versions to lint
the v2 schema and generate deterministic Go clients. `scripts/test-generated-api.sh`
regenerates those clients, compares the schema with the immutable Candidate
descriptor baseline, and compiles the generated package. Generated files are
committed so release builds do not depend on the schema registry. The legacy
v1 schema remains outside STANDARD lint while mixed-version compatibility is
required.

The production gate runs policy, reproducibility, compilation, and breaking
checks on every change. Buf breaking-change detection compares v2 with its
immutable Candidate baseline.

Related: [control-plane security](control-plane-security.md),
[compatibility matrix](../release/compatibility-matrix.md), and
[architecture container view](../architecture/container-view.md).
