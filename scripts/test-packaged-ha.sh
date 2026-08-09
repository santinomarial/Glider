#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v go >/dev/null 2>&1; then
	exec docker run --rm -v "${REPO_ROOT}:/src" -v glider-go-mod:/go/pkg/mod -v glider-go-build:/root/.cache/go-build -w /src golang:1.26 bash scripts/test-packaged-ha.sh
fi
if [ -n "$(git -C "${REPO_ROOT}" status --porcelain)" ]; then
	echo "packaged HA qualification requires a clean Git worktree" >&2
	exit 2
fi
WORK="$(mktemp -d)"
KEY="${WORK}/signing.pem"
openssl genpkey -algorithm Ed25519 -out "${KEY}"
GLIDER_SIGNING_KEY="${KEY}" "${REPO_ROOT}/scripts/release.sh"
VERSION="$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")"
ARCH="$(go env GOARCH)"
tar -xzf "${REPO_ROOT}/dist/glider-${VERSION}-linux-${ARCH}.tar.gz" -C "${WORK}"
GLIDER_HA_CONTROLPLANE="${WORK}/glider-${VERSION}-linux-${ARCH}/bin/glider-controlplane" \
	go test -v -timeout 120s ./test/integration/ha
