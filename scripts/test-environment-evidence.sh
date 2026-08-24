#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
EVIDENCE="${WORK}/evidence"
mkdir -p "${EVIDENCE}"
docker run --rm -v "${WORK}:/work" golang:1.26.6 sh -c \
	'openssl genpkey -algorithm Ed25519 -out /work/reviewer.key && openssl pkey -in /work/reviewer.key -pubout -out /work/reviewer.pem'
{
	printf 'source_commit=%s\n' "$(git -C "${REPO_ROOT}" rev-parse HEAD)"
	printf 'independent_reviewer=%s\n' 'contract-test-reviewer'
	printf 'security_decision=%s\n' 'approved'
} > "${EVIDENCE}/manifest.txt"
while IFS=$'\t' read -r scenario checks; do
	: > "${EVIDENCE}/${scenario}.log"
	while IFS= read -r check; do
		printf 'GLIDER_ASSERT scenario=%s check=%s result=pass observed=contract-test\n' "${scenario}" "${check}" >> "${EVIDENCE}/${scenario}.log"
	done < <(printf '%s\n' "${checks}" | tr ',' '\n')
done < "${REPO_ROOT}/qualification/required-checks.tsv"
printf 'independent threat review approved for contract test\n' > "${EVIDENCE}/security-review.md"
docker run --rm -v "${WORK}:/work" -w /work/evidence golang:1.26.6 sh -c \
	'find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.sig -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS && openssl pkeyutl -sign -rawin -inkey ../reviewer.key -in SHA256SUMS -out SHA256SUMS.sig'
GLIDER_ENVIRONMENT_EVIDENCE_PUBLIC_KEY="${WORK}/reviewer.pem" "${REPO_ROOT}/scripts/verify-environment-evidence.sh" "${EVIDENCE}"
printf 'tampered\n' >> "${EVIDENCE}/network-qualification.log"
if GLIDER_ENVIRONMENT_EVIDENCE_PUBLIC_KEY="${WORK}/reviewer.pem" "${REPO_ROOT}/scripts/verify-environment-evidence.sh" "${EVIDENCE}" >/dev/null 2>&1; then
	echo "tampered environment evidence was accepted" >&2
	exit 1
fi
echo "ENVIRONMENT EVIDENCE CONTRACT GREEN: valid signatures pass and tampering fails"
