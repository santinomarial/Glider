#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEGACY_REF="${GLIDER_UPGRADE_LEGACY_REF:-4341694}"
if [ "$(uname -s)" != Linux ] || ! command -v go >/dev/null 2>&1; then
	exec docker run --rm -v "${REPO_ROOT}:/src" -v glider-go-mod:/go/pkg/mod -v glider-go-build:/root/.cache/go-build -w /src golang:1.26 bash scripts/test-upgrade.sh
fi
if [ -n "$(git -C "${REPO_ROOT}" status --porcelain)" ]; then
	echo "upgrade qualification requires a clean Git worktree" >&2
	exit 2
fi
command -v openssl >/dev/null
ARCH="$(go env GOARCH)"
WORK="$(mktemp -d)"
KEY="${WORK}/signing.pem"
openssl genpkey -algorithm Ed25519 -out "${KEY}"
GLIDER_SIGNING_KEY="${KEY}" "${REPO_ROOT}/scripts/release.sh"
tar -xzf "${REPO_ROOT}/dist/glider-$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")-linux-${ARCH}.tar.gz" -C "${WORK}"
PACKAGE="${WORK}/glider-$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")-linux-${ARCH}"
LEGACY="${WORK}/legacy"
mkdir -p "${LEGACY}"
git -C "${REPO_ROOT}" archive "${LEGACY_REF}" | tar -x -C "${LEGACY}"
(cd "${LEGACY}" && go build -trimpath -o "${WORK}/glider-controlplane-legacy" ./cmd/glider-controlplane)
GLIDER_UPGRADE_CURRENT_CONTROLPLANE="${PACKAGE}/bin/glider-controlplane" \
GLIDER_UPGRADE_CURRENT_ADMIN="${PACKAGE}/bin/glider-admin" \
GLIDER_UPGRADE_LEGACY_CONTROLPLANE="${WORK}/glider-controlplane-legacy" \
	go test -v -timeout 120s ./test/integration/upgrade
