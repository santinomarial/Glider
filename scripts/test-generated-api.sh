#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

scripts/run-buf.sh lint . --path api/proto/glider/v2
scripts/run-buf.sh breaking . \
	--path api/proto/glider/v2 \
	--against api/proto/baseline/v2.binpb \
	--limit-to-input-files
scripts/generate-api.sh
git diff --exit-code -- api/gen
go test ./api/gen/glider/v2

echo "GENERATED API GREEN: v2 is reproducible, compatible, and compilable"
