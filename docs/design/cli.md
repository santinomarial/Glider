# CLI

`glider` is the operator client for the versioned control-plane gRPC API. It
supports `run`, `deploy`, `scale`, `nodes`, `ps`, `inspect`, and `events`, emits
stable JSON suitable for scripts, uses `GLIDER_ENDPOINT` or `--endpoint`, and
bounds every request with `--timeout`. `logs`, `exec`, and `stats` are reserved
commands that fail explicitly until the authenticated node streaming API is
implemented; they never silently fall back to unsafe local filesystem access.
