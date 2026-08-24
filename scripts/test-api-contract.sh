#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

go test ./tools/proto-policy
go run ./tools/proto-policy -root api/proto/glider/v2
