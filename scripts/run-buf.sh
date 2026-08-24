#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUF_VERSION="1.72.0"
cd "${REPO_ROOT}"

if command -v buf >/dev/null 2>&1; then
	if [ "$(buf --version)" != "${BUF_VERSION}" ]; then
		echo "buf ${BUF_VERSION} is required" >&2
		exit 2
	fi
	exec buf "$@"
fi

command -v docker >/dev/null 2>&1 || {
	echo "buf ${BUF_VERSION} or Docker is required" >&2
	exit 2
}
exec docker run --rm -v "${REPO_ROOT}:/workspace" -w /workspace \
	"bufbuild/buf:${BUF_VERSION}" "$@"
