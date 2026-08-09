#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EVIDENCE="${1:-}"
"${REPO_ROOT}/scripts/verify-environment-evidence.sh" "${EVIDENCE}"
"${REPO_ROOT}/scripts/production-gate.sh"
mkdir -p "${REPO_ROOT}/dist/evidence/environment"
cp "${EVIDENCE}"/* "${REPO_ROOT}/dist/evidence/environment/"
echo "PRODUCTION RELEASE GREEN: local and independently signed environment evidence are complete"
