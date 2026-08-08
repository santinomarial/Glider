#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v go >/dev/null 2>&1; then
	exec docker run --rm -v "${REPO_ROOT}:/src:ro" -w /src golang:1.26 /usr/local/go/bin/go test -run '^$' -bench . -benchmem ./internal/scheduler ./internal/discovery
fi
cd "${REPO_ROOT}"
go test -run '^$' -bench . -benchmem ./internal/scheduler ./internal/discovery
