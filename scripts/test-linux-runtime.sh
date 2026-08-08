#!/usr/bin/env bash
# Reproducible entry point for Glider's privileged Linux runtime test
# suite (Phase 2 gap noted, Phase 4 §29): runs the real Go toolchain
# against a real Linux kernel with cgroup v2, root/privileged, and
# executes the unit + privileged integration test suites, returning the
# real test status — usable both locally (on a Linux host) and via a
# throwaway container on non-Linux dev machines (macOS/Windows via Docker
# Desktop; see the DOCKER_IMAGE section below).
#
# Usage:
#   scripts/test-linux-runtime.sh                 # unit + integration, default
#   scripts/test-linux-runtime.sh -run TestFoo     # extra args forwarded to `go test`
#   GLIDER_TEST_STRESS=1 scripts/test-linux-runtime.sh   # also run the leak/stress loop
#
# On a real Linux host with root, run this script directly. On macOS/
# Windows, it re-execs itself inside a privileged Linux container (Docker
# Desktop's own Linux VM) — no Docker-in-Docker, just one level of
# containerization, matching Phase 4 §29's "do not introduce Docker-in-
# Docker complexity unless required".
set -euo pipefail

export GLIDER_REQUIRE_PRIVILEGED_TESTS=1

DOCKER_IMAGE="golang:1.26"
DOCKER_PLATFORM="${GLIDER_TEST_PLATFORM:-linux/amd64}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ "$(uname -s)" != "Linux" ]; then
	echo "==> Not running on Linux ($(uname -s)) — re-executing inside a privileged ${DOCKER_IMAGE} container"
	exec docker run --rm --privileged --platform "${DOCKER_PLATFORM}" \
		-v "${REPO_ROOT}:/source:ro" --mount type=volume,dst=/work -w /work \
		-e GLIDER_TEST_STRESS \
		"${DOCKER_IMAGE}" \
		bash -c 'tar --exclude=./work -C /source -cf - . | tar -C /work -xf - && exec bash scripts/test-linux-runtime.sh "$@"' bash "$@"
fi

if [ "$(id -u)" -ne 0 ]; then
	echo "error: must run as root (privileged) for namespace/mount/pivot_root/cgroup operations" >&2
	exit 1
fi

echo "== environment =="
echo "kernel:      $(uname -r)"
echo "arch:        $(uname -m)"
if mount | grep -q 'type cgroup2'; then
	echo "cgroup mode: v2 (unified)"
else
	echo "cgroup mode: NOT v2 — this test suite requires cgroup v2 (ADR-0001)" >&2
	exit 1
fi
echo "controllers: $(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null || echo unknown)"

# One-time cgroup delegation bootstrap, performed here — as the true
# process-tree root of this test run — rather than by the first `go test`
# subprocess that happens to need it (docs/design/cgroups.md
# "Delegation"): cgroup v2's "no internal process" constraint means
# whichever process enables cgroup.subtree_control must not itself share
# that cgroup with other live processes. In a bare `docker run --privileged
# ... bash -c "go test ..."` invocation, `go test` (and every glider-runtime
# subprocess it spawns) is a *descendant* of this very shell — so THIS
# shell, as the actual sole occupant of the cgroup at this point, is the
# right (and only reliably available) place to do it once. Every
# subsequent EnsureDelegated call (inside `go test`, and inside every
# glider-runtime subprocess under test) then takes the already-delegated
# fast path. This is not a Glider bug or a workaround baked into
# production code — cmd/glider-runtime never runs this script; it's this
# harness's job specifically because of how nested test invocation shares
# a cgroup with its own ancestor shell (Phase 4 §28).
CGROUP_MOUNT="$(awk '{for(i=1;i<=NF;i++) if ($i=="-" && $(i+1)=="cgroup2") {print $5; exit}}' /proc/self/mountinfo)"
if [ -z "${CGROUP_MOUNT}" ]; then
	echo "error: no cgroup2 mount found" >&2
	exit 1
fi
GLIDER_CGROUP="${CGROUP_MOUNT}/glider"
BOOTSTRAP_LEAF="${GLIDER_CGROUP}/_supervisor"
mkdir -p "${BOOTSTRAP_LEAF}"
echo $$ > "${BOOTSTRAP_LEAF}/cgroup.procs" || true
# Both writes are idempotent (re-enabling an already-enabled controller is
# a kernel no-op success) and best-effort here: if this genuinely can't
# succeed (real delegation failure, not just "already done"), Glider's own
# EnsureDelegated call inside the test suite reports ErrNotDelegated
# authoritatively — this script's job is only to make the common case
# (nothing delegated yet) work, not to be the final word on it.
echo "+cpu +memory +pids" > "${CGROUP_MOUNT}/cgroup.subtree_control" 2>/dev/null || true
echo "+cpu +memory +pids" > "${GLIDER_CGROUP}/cgroup.subtree_control" 2>/dev/null || true
echo "delegation:  $(cat "${GLIDER_CGROUP}/cgroup.subtree_control" 2>/dev/null || echo 'not yet available — EnsureDelegated will report ErrNotDelegated if this genuinely cannot be fixed')"
echo

cd "${REPO_ROOT}"

# Go's t.TempDir follows TMPDIR. Under Docker Desktop, /tmp is part of the
# container's overlay root; using it as the backing store for Glider's own
# OverlayFS snapshots creates an unsupported overlay-on-overlay mount. Keep all
# test temporaries on the Linux-native work volume/filesystem instead.
export TMPDIR="${REPO_ROOT}/work/test-tmp"
mkdir -p "${TMPDIR}"

echo "== gofmt =="
# `work/` contains the locally cached Go distribution and module/build caches.
# The Go distribution intentionally includes syntactically invalid parser test
# fixtures, so scanning the entire repository makes this check fail before it
# reaches Glider. Restrict formatting verification to project source.
UNFORMATTED="$(find . -path './work' -prune -o -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
if [ -n "${UNFORMATTED}" ]; then
	echo "not gofmt-clean:" >&2
	echo "${UNFORMATTED}" >&2
	exit 1
fi
echo "clean"
echo

echo "== go build ./... =="
go build ./...
echo "== go vet ./... =="
go vet ./...
echo

echo "== go test ./internal/... (unit) =="
go test ./internal/...
echo "== go test -race ./internal/... =="
go test -race ./internal/...
echo

echo "== go test -v ./test/integration/runtime/... (privileged integration) =="
go test -v -timeout 600s ./test/integration/runtime/... "$@"
echo

if [ "${GLIDER_TEST_STRESS:-0}" = "1" ]; then
	echo "== leak/stress: 20x full integration suite =="
	for i in $(seq 1 20); do
		echo "--- stress iteration ${i} ---"
		go test -count=1 -timeout 600s ./test/integration/runtime/...
	done
fi

echo "ALL GREEN"
