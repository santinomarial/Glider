#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EVIDENCE="${1:-}"
PUBLIC_KEY="${GLIDER_ENVIRONMENT_EVIDENCE_PUBLIC_KEY:-}"
if [ -z "${EVIDENCE}" ] || [ ! -d "${EVIDENCE}" ]; then
	echo "usage: verify-environment-evidence.sh EVIDENCE_DIR" >&2
	exit 2
fi
if [ -z "${PUBLIC_KEY}" ] || [ ! -f "${PUBLIC_KEY}" ]; then
	echo "GLIDER_ENVIRONMENT_EVIDENCE_PUBLIC_KEY must name the trusted independent-review Ed25519 public key" >&2
	exit 2
fi
for file in manifest.txt SHA256SUMS SHA256SUMS.sig multi-host-lb.log off-host-restore.log rolling-upgrade.log node-replacement.log disk-pressure.log network-qualification.log monitoring-delivery.log security-review.md; do
	if [ ! -s "${EVIDENCE}/${file}" ]; then
		echo "required environment evidence is missing or empty: ${file}" >&2
		exit 1
	fi
done
expected="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
actual="$(sed -n 's/^source_commit=//p' "${EVIDENCE}/manifest.txt")"
decision="$(sed -n 's/^security_decision=//p' "${EVIDENCE}/manifest.txt")"
reviewer="$(sed -n 's/^independent_reviewer=//p' "${EVIDENCE}/manifest.txt")"
if [ "${actual}" != "${expected}" ]; then
	echo "environment evidence commit ${actual:-missing} does not match ${expected}" >&2
	exit 1
fi
if [ "${decision}" != approved ] || [ -z "${reviewer}" ]; then
	echo "independent security approval is missing" >&2
	exit 1
fi
docker run --rm -v "${EVIDENCE}:/evidence:ro" -v "${PUBLIC_KEY}:/reviewer.pem:ro" -w /evidence golang:1.26 \
	sh -c 'sha256sum --check SHA256SUMS && openssl pkeyutl -verify -pubin -inkey /reviewer.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig'
echo "ENVIRONMENT EVIDENCE GREEN: multi-host qualification and independent review are bound to ${expected}"
