#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v go >/dev/null 2>&1; then
	exec docker run --rm -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work golang:1.26.6 bash -c 'tar --exclude=./work -C /src -cf - . | tar -C /work -xf - && /usr/local/go/bin/go run ./cmd/glider-demo'
fi
cd "${REPO_ROOT}"; go run ./cmd/glider-demo
