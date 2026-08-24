#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ "$(uname -s)" != Linux ]; then
	exec docker run --rm --privileged -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work golang:1.26.6 bash -c \
		'tar -C /src -cf - . | tar -C /work -xf - && exec bash scripts/test-security.sh'
fi
cd "${REPO_ROOT}"
exec bash scripts/test-linux-runtime.sh -run 'TestProcShowsOnlyContainerProcessTree|TestUTSHostnameIsolation|TestRootFilesystemIsolation|TestInvalidWorkloadReturnsClearError'
echo "SECURITY GREEN: integrity, traversal, isolation, no-new-privileges, and seccomp gates held"
