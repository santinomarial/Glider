#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${PROMETHEUS_IMAGE:-prom/prometheus:v3.7.3}"
docker run --rm \
	-v "${REPO_ROOT}/packaging/monitoring:/monitoring:ro" \
	-w /monitoring --entrypoint promtool "${IMAGE}" \
	check rules glider.rules.yml
docker run --rm \
	-v "${REPO_ROOT}/packaging/monitoring:/monitoring:ro" \
	-w /monitoring --entrypoint promtool "${IMAGE}" \
	test rules glider.rules.test.yml
