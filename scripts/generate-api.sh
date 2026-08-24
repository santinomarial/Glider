#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUF_IMAGE="bufbuild/buf:1.72.0"
cd "${REPO_ROOT}"

docker run --rm -v "${REPO_ROOT}:/workspace" -w /workspace "${BUF_IMAGE}" lint
docker run --rm -v "${REPO_ROOT}:/workspace" -w /workspace "${BUF_IMAGE}" generate

echo "API GENERATED: typed Go messages and gRPC clients are synchronized"
