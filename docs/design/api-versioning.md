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

The typed v2 contract begins in **Draft** state. It becomes Candidate after
pinned generation and generated-client compilation pass, and Current only
after its server adapter, CLI, node agent, and mixed-version tests pass
together. The legacy Struct-based v1 API must not be removed in the same
release that first serves v2.

## Generation and policy checks

`scripts/test-api-contract.sh` performs offline repository policy checks.
`scripts/generate-api.sh` uses pinned Buf and Go/gRPC plugin versions to lint
the complete protobuf schema and generate deterministic Go clients. Generated
files are committed so release builds do not depend on the schema registry.

The production gate runs policy checks on every change. Once the v2 API enters
Current state, Buf breaking-change detection compares it with the immutable v2
release baseline.

Related: [control-plane security](control-plane-security.md),
[compatibility matrix](../release/compatibility-matrix.md), and
[architecture container view](../architecture/container-view.md).
