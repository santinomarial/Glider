#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ITERATIONS="${GLIDER_CHAOS_ITERATIONS:-25}"
if [ "$(uname -s)" != Linux ]; then
	exec docker run --rm -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work \
		-e GLIDER_CHAOS_ITERATIONS golang:1.26.6 bash -c \
		'tar -C /src -cf - . | tar -C /work -xf - && exec bash scripts/test-chaos.sh'
fi
cd "${REPO_ROOT}"
echo "Running ${ITERATIONS} randomized/repeated failure convergence iterations"
go test -race -count="${ITERATIONS}" ./internal/agent ./internal/controller/workload ./internal/controller/service ./internal/lease
go test -count="${ITERATIONS}" ./internal/store/etcd -run 'TestEtcdConcurrentBindHasOneWinner|TestEvictUnreachableNodeRequeuesWithNewGeneration|TestDeleteAssignedTaskAtomicallyReleasesReservation'
echo "CHAOS GREEN: duplicate reconcile, stale bind, node eviction, and restart recovery invariants held"
