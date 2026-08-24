#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MERMAID_IMAGE="ghcr.io/mermaid-js/mermaid-cli/mermaid-cli:11.15.0"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
chmod 0777 "${WORK}"

docker run --rm -v "${REPO_ROOT}:/src:ro" -v "${WORK}:/out" -w /src \
	golang:1.26.6 go run ./tools/docs-check -root /src -mermaid-dir /out

for source in "${WORK}"/*.mmd; do
	name="$(basename "${source}" .mmd)"
	echo "validating ${name}"
	docker run --rm -v "${WORK}:/data" "${MERMAID_IMAGE}" \
		-i "/data/${name}.mmd" -o "/data/${name}.svg" >/dev/null
done

echo "MERMAID GREEN: every architecture diagram rendered with mermaid-cli 11.15.0"
