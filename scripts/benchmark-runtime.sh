#!/usr/bin/env bash
set -euo pipefail
DOCKER_IMAGE="golang:1.26"
case "$(uname -m)" in arm64|aarch64) DEFAULT_DOCKER_PLATFORM="linux/arm64" ;; *) DEFAULT_DOCKER_PLATFORM="linux/amd64" ;; esac
DOCKER_PLATFORM="${GLIDER_TEST_PLATFORM:-${DEFAULT_DOCKER_PLATFORM}}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GLIDER_SOURCE_COMMIT="${GLIDER_SOURCE_COMMIT:-$(git -C "${REPO_ROOT}" rev-parse HEAD)}"
export GLIDER_SOURCE_COMMIT
if [ "$(uname -s)" != "Linux" ]; then
	exec docker run --rm --privileged --platform "${DOCKER_PLATFORM}" -v "${REPO_ROOT}:/source:ro" --mount type=volume,dst=/work -w /work -e GLIDER_SOURCE_COMMIT -e GLIDER_RUNTIME_PERFORMANCE_ITERATIONS -e GLIDER_RUNTIME_LIFECYCLE_P99_MAX "${DOCKER_IMAGE}" bash -c 'tar --exclude=./.git --exclude=./dist --exclude=./work -C /source -cf - . | tar -C /work -xf - && exec bash scripts/benchmark-runtime.sh'
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "error: runtime benchmark requires root and cgroup v2" >&2
	exit 1
fi
CGROUP_MOUNT="$(awk '{for(i=1;i<=NF;i++) if ($i=="-" && $(i+1)=="cgroup2") {print $5; exit}}' /proc/self/mountinfo)"
if [ -z "${CGROUP_MOUNT}" ]; then
	echo "error: runtime benchmark requires cgroup v2" >&2
	exit 1
fi
GLIDER_CGROUP="${CGROUP_MOUNT}/glider"
mkdir -p "${GLIDER_CGROUP}/_supervisor"
echo $$ > "${GLIDER_CGROUP}/_supervisor/cgroup.procs" || true
echo "+cpu +memory +pids" > "${CGROUP_MOUNT}/cgroup.subtree_control" 2>/dev/null || true
echo "+cpu +memory +pids" > "${GLIDER_CGROUP}/cgroup.subtree_control" 2>/dev/null || true
cd "${REPO_ROOT}"
export TMPDIR="${REPO_ROOT}/work/runtime-benchmark-tmp"
mkdir -p "${TMPDIR}"
export GLIDER_RUNTIME_PERFORMANCE=1
go test -v -count=1 -timeout 180s ./test/integration/runtime -run '^TestRuntimeLifecyclePerformance$'
