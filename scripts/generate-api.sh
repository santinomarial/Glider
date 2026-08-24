#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

scripts/run-buf.sh lint . --path api/proto/glider/v2
scripts/run-buf.sh generate --path api/proto/glider/v2

echo "API GENERATED: typed Go messages and gRPC clients are synchronized"
