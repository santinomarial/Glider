# ADR 0002: OCI image model — own the mechanism, use spec types as data

## Status

Accepted.

## Context

Glider needs to pull OCI images: resolve a reference against a registry,
fetch manifests/config/layers, verify digests, store content, unpack
layers, and construct a root filesystem (master plan §13–§15). The OCI
image and distribution specs are well-defined JSON schemas with an existing
Go ecosystem: `opencontainers/image-spec`'s `specs-go` package (plain struct
definitions for manifests/configs — no behavior), `opencontainers/go-digest`
(digest parsing/verification helpers), versus full engines like
`google/go-containerregistry` or `containerd/containerd`'s content/image
packages, which implement registry clients, content stores, and unpack
logic end-to-end.

## Problem

Master plan §13 is explicit: "Do not outsource this entire path to Docker
or containerd." But refusing *every* external package, including plain
struct definitions for a stable public JSON schema, would mean
hand-transcribing the OCI spec's field names and JSON tags — busywork that
adds no systems-engineering depth and creates a maintenance burden if the
spec adds fields Glider needs to at least tolerate (forward-compatible
JSON handling). The line has to be drawn somewhere between "use the spec's
data shapes" and "use someone else's engine."

## Decision

Glider **implements the mechanism itself**: the registry HTTP client
(manifest/blob fetch, auth challenge handling), the content-addressed blob
store (layout, atomic publication, digest verification), the layer
unpacker (tar walking, whiteout handling, path-traversal defense), and the
OverlayFS snapshotter. These are the parts with real Linux/systems depth
and are where the learning goal of the project actually lives.

Glider **may use** `opencontainers/image-spec`'s struct definitions and
`opencontainers/go-digest`'s digest type/parsing as data types — these
encode a stable public specification, not runtime behavior, and using them
is equivalent to using `encoding/json` itself: it saves transcribing a spec
without delegating any actual work. Glider does **not** use
`go-containerregistry`, `containerd/containerd`, or any package that
performs registry resolution, content storage, or unpacking on Glider's
behalf — those are exactly the mechanisms this project exists to build and
understand.

Scope for the initial implementation (master plan §13, restated as the
concrete subset): resolve a `name:tag` or `name@digest` reference against a
v2 registry, fetch and digest-verify the manifest/index, image config, and
layer blobs, store them content-addressed, unpack layers with correct
whiteout/opaque-directory semantics, and extract entrypoint/env/working-dir
from the image config. Multi-arch index selection is limited to the single
target platform (`linux/amd64`, per master plan §4) rather than full
multi-platform handling. Signature verification (cosign-style) is out of
scope for the initial implementation — not a rejected idea, just not yet
justified against the phase's exit gate.

## Alternatives considered

- **Full external engine** (`go-containerregistry` or containerd's content
  packages) for the whole pull path. Rejected: directly contradicts the
  project's stated purpose — it would replace the exact subsystem (Phase
  5–7) meant to demonstrate content-addressed storage and layer unpacking
  depth.
- **Hand-write every struct**, no spec-types dependency at all. Rejected as
  needless purity: the OCI struct shapes are a public specification, not an
  implementation to learn from; transcribing them by hand adds risk (typos
  in JSON tags silently breaking compatibility) without adding
  understanding.
- **Vendor a minimal hand-copied subset of the spec structs** instead of
  depending on the upstream module. Considered viable but rejected in favor
  of depending on the upstream types directly: it's the actual spec,
  maintained by the spec's own stewards, and reduces the chance of a subtle
  divergence from real-world registries' behavior.

## Consequences

- `go.mod` will carry `opencontainers/image-spec` and `opencontainers/go-digest`
  as dependencies; no other OCI/registry/runtime engine packages.
- Registry HTTP interaction (including auth challenge/token flows per the
  distribution spec) is Glider's own code, giving direct control over and
  insight into that protocol.
- The content store, unpacker, and snapshotter are fully Glider's own
  design (their own docs and ADR-0004 for the snapshotter specifically).

## Risks

- Hand-rolling the registry client means reimplementing auth-challenge
  handling (WWW-Authenticate parsing, bearer token exchange) that mature
  clients already got right through real-world iteration; bugs here are
  plausible and should be caught by integration tests against a real
  registry (e.g. a local registry:2 instance) rather than assumed away.

## What would cause reconsideration

If registry-protocol edge cases (unusual auth flows, nonstandard registries)
consume disproportionate time relative to the learning value, narrowing to
a smaller registry compatibility target (e.g. Docker Hub + GHCR + a local
registry, explicitly not "all OCI-compliant registries") would be the
correct fix — documented here as the fallback, not a change to the
own-the-mechanism decision itself.
