#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DURATION="${GLIDER_FUZZ_TIME:-30s}"

if [ "$(uname -s)" != Linux ]; then
	exec docker run --rm -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work \
		-e GLIDER_FUZZ_TIME="${DURATION}" golang:1.26 bash -c \
		'tar -C /src -cf - . | tar -C /work -xf - && exec bash scripts/test-fuzz.sh'
fi

cd "${REPO_ROOT}"
go test ./internal/admission -run='^$' -fuzz=FuzzAdmissionJSON -fuzztime="${DURATION}"
