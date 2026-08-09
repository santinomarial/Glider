#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v go >/dev/null 2>&1; then
	exec docker run --rm -v "${REPO_ROOT}:/src:ro" --mount type=volume,dst=/work -w /work \
		-e GLIDER_SCHEDULE_NS_MAX -e GLIDER_SCHEDULE_BYTES_MAX -e GLIDER_LOOKUP_NS_MAX -e GLIDER_LOOKUP_BYTES_MAX \
		golang:1.26 sh -c 'tar --exclude=./dist --exclude=./work -C /src -cf - . | tar -C /work -xf - && exec bash scripts/benchmark.sh'
fi
cd "${REPO_ROOT}"
SCHEDULE_NS_MAX="${GLIDER_SCHEDULE_NS_MAX:-500000}"
SCHEDULE_BYTES_MAX="${GLIDER_SCHEDULE_BYTES_MAX:-600000}"
LOOKUP_NS_MAX="${GLIDER_LOOKUP_NS_MAX:-250}"
LOOKUP_BYTES_MAX="${GLIDER_LOOKUP_BYTES_MAX:-128}"
RESULTS="$(go test -run '^$' -bench . -benchmem ./internal/scheduler ./internal/discovery)"
printf '%s\n' "${RESULTS}"
printf '%s\n' "${RESULTS}" | awk \
	-v schedule_ns="${SCHEDULE_NS_MAX}" -v schedule_bytes="${SCHEDULE_BYTES_MAX}" \
	-v lookup_ns="${LOOKUP_NS_MAX}" -v lookup_bytes="${LOOKUP_BYTES_MAX}" '
/^BenchmarkSchedule1000Nodes-/ { seen_schedule=1; if ($(NF-5) > schedule_ns || $(NF-3) > schedule_bytes) { print "scheduler benchmark exceeded production envelope" > "/dev/stderr"; failed=1 } }
/^BenchmarkLookup1000Endpoints-/ { seen_lookup=1; if ($(NF-5) > lookup_ns || $(NF-3) > lookup_bytes) { print "discovery benchmark exceeded production envelope" > "/dev/stderr"; failed=1 } }
END { if (!seen_schedule || !seen_lookup) { print "required production benchmark missing" > "/dev/stderr"; failed=1 }; exit failed }
'
echo "PERFORMANCE GREEN: scheduler and discovery stayed within the production envelope"
