#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"
if [ -n "$(git status --porcelain)" ]; then
	echo "production gate requires a clean Git worktree" >&2
	exit 2
fi
command -v docker >/dev/null

WORK="$(mktemp -d)"
LOGS="${WORK}/logs"
KEY="${WORK}/release.key"
mkdir -p "${LOGS}"
docker run --rm -v "${WORK}:/work" golang:1.26 \
	openssl genpkey -algorithm Ed25519 -out /work/release.key >/dev/null 2>&1

run_gate() {
	local name="$1"
	shift
	echo "==> ${name}"
	"$@" 2>&1 | tee "${LOGS}/${name}.log"
}

run_gate linux-runtime env GLIDER_TEST_STRESS="${GLIDER_TEST_STRESS:-1}" scripts/test-linux-runtime.sh
run_gate documentation scripts/test-docs.sh
run_gate security scripts/test-security.sh
run_gate vulnerabilities scripts/test-vulnerabilities.sh
run_gate admission-fuzz env GLIDER_FUZZ_TIME="${GLIDER_FUZZ_TIME:-60s}" scripts/test-fuzz.sh
run_gate convergence-chaos env GLIDER_CHAOS_ITERATIONS="${GLIDER_CHAOS_ITERATIONS:-50}" scripts/test-chaos.sh
run_gate monitoring scripts/test-monitoring.sh
run_gate environment-qualification-contract scripts/test-environment-qualification.sh
run_gate environment-evidence-contract scripts/test-environment-evidence.sh
run_gate backup-recovery docker run --rm -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work golang:1.26 sh -c 'tar -C /src -cf - . | tar -C /work -xf - && go test -race -v ./test/integration/backup ./test/integration/secrets'
run_gate packaged-ha scripts/test-packaged-ha.sh
run_gate packaged-upgrade scripts/test-upgrade.sh
run_gate benchmarks scripts/benchmark.sh
run_gate runtime-benchmark scripts/benchmark-runtime.sh

run_gate signed-release env GLIDER_SIGNING_KEY="${KEY}" docker run --rm -v "${REPO_ROOT}:/src" -v "${KEY}:/release.key:ro" -w /src -e GLIDER_SIGNING_KEY=/release.key golang:1.26 sh -c 'bash scripts/release.sh && bash scripts/verify-release.sh dist'

EVIDENCE="${REPO_ROOT}/dist/evidence"
mkdir -p "${EVIDENCE}/logs"
cp "${LOGS}"/*.log "${EVIDENCE}/logs/"
cp docs/release/compatibility-matrix.md "${EVIDENCE}/compatibility-matrix.md"
{
	printf 'source_commit=%s\n' "$(git rev-parse HEAD)"
	printf 'version=%s\n' "$(tr -d '[:space:]' < VERSION)"
	printf 'completed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	printf 'host=%s\n' "$(uname -srm)"
	printf 'go_image=%s\n' 'golang:1.26'
	printf 'prometheus_image=%s\n' 'prom/prometheus:v3.7.3'
} > "${EVIDENCE}/manifest.txt"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "${EVIDENCE}" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
else
	(cd "${EVIDENCE}" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 > SHA256SUMS)
fi
echo "PRODUCTION GATE GREEN: evidence written to ${EVIDENCE}"
