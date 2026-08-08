# CLI

`glider` is the operator client for the versioned control-plane gRPC API. It
supports `run`, `deploy`, `scale`, `nodes`, `ps`, `inspect`, and `events`, emits
stable JSON suitable for scripts, uses `GLIDER_ENDPOINT` or `--endpoint`, and
bounds every request with `--timeout`. `logs` and `stats` resolve a task's
generation-fenced node endpoint and use its authenticated operations API.
`exec` remains unavailable until the same transport safely supports an
interactive stream; it never falls back to unsafe local filesystem access.
