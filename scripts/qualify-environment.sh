#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLAN="${1:-}"
OUTPUT="${2:-}"
if [ -z "${PLAN}" ] || [ ! -d "${PLAN}" ] || [ -z "${OUTPUT}" ]; then
	echo "usage: qualify-environment.sh PLAN_DIR ABSOLUTE_OUTPUT_DIR" >&2
	exit 2
fi
if [ "${OUTPUT#/}" = "${OUTPUT}" ] || [ "${OUTPUT}" = / ] || [ -e "${OUTPUT}" ]; then
	echo "output must be a new absolute directory other than /" >&2
	exit 2
fi
CONFIG="${PLAN}/plan.env"
if [ ! -f "${CONFIG}" ]; then
	echo "qualification plan is missing plan.env" >&2
	exit 1
fi

read_value() {
	local key="$1" count value
	count="$(grep -c "^${key}=" "${CONFIG}" || true)"
	if [ "${count}" -ne 1 ]; then
		echo "plan.env must contain exactly one ${key}= entry" >&2
		exit 1
	fi
	value="$(sed -n "s/^${key}=//p" "${CONFIG}")"
	if [ -z "${value}" ] || printf '%s' "${value}" | grep -q '[[:cntrl:]]'; then
		echo "plan.env has an invalid ${key}" >&2
		exit 1
	fi
	printf '%s' "${value}"
}

ENVIRONMENT_ID="$(read_value ENVIRONMENT_ID)"
CONTROL_PLANE_HOSTS="$(read_value CONTROL_PLANE_HOSTS)"
WORKER_HOSTS="$(read_value WORKER_HOSTS)"
LOAD_BALANCER_ENDPOINT="$(read_value LOAD_BALANCER_ENDPOINT)"
OFF_HOST_BACKUP_URI="$(read_value OFF_HOST_BACKUP_URI)"
MONITORING_RECEIVER="$(read_value MONITORING_RECEIVER)"
CERTIFICATE_MANAGER="$(read_value CERTIFICATE_MANAGER)"

validate_hosts() {
	local label="$1" value="$2" minimum="$3" total unique
	total="$(printf '%s\n' "${value}" | tr ',' '\n' | sed '/^$/d' | wc -l | tr -d ' ')"
	unique="$(printf '%s\n' "${value}" | tr ',' '\n' | sed '/^$/d' | sort -u | wc -l | tr -d ' ')"
	if [ "${total}" -lt "${minimum}" ] || [ "${total}" -ne "${unique}" ]; then
		echo "${label} requires at least ${minimum} unique hosts" >&2
		exit 1
	fi
}
validate_hosts CONTROL_PLANE_HOSTS "${CONTROL_PLANE_HOSTS}" 3
validate_hosts WORKER_HOSTS "${WORKER_HOSTS}" 2
case "${LOAD_BALANCER_ENDPOINT}" in localhost:*|127.*|\[::1\]:*) echo "load balancer endpoint must not be loopback" >&2; exit 1 ;; esac
case "${OFF_HOST_BACKUP_URI}" in s3://*|gs://*|https://*|azure://*) ;; *) echo "off-host backup URI must use a remote immutable-storage scheme" >&2; exit 1 ;; esac

mkdir -p "${OUTPUT}"
SOURCE_COMMIT="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
export ENVIRONMENT_ID CONTROL_PLANE_HOSTS WORKER_HOSTS LOAD_BALANCER_ENDPOINT OFF_HOST_BACKUP_URI MONITORING_RECEIVER CERTIFICATE_MANAGER SOURCE_COMMIT
while IFS=$'\t' read -r scenario checks; do
	script="${PLAN}/${scenario}"
	log="${OUTPUT}/${scenario}.log"
	if [ ! -x "${script}" ]; then
		echo "required executable scenario is missing: ${scenario}" >&2
		exit 1
	fi
	{
		printf 'source_commit=%s\n' "${SOURCE_COMMIT}"
		printf 'environment_id=%s\n' "${ENVIRONMENT_ID}"
		printf 'scenario=%s\n' "${scenario}"
	} > "${log}"
	if ! "${script}" 2>&1 | tee -a "${log}"; then
		echo "environment scenario failed: ${scenario}" >&2
		exit 1
	fi
	while IFS= read -r check; do
		marker="GLIDER_ASSERT scenario=${scenario} check=${check} result=pass observed="
		count="$(awk -v marker="${marker}" 'index($0, marker) == 1 && length($0) > length(marker) { count++ } END { print count + 0 }' "${log}")"
		if [ "${count}" -ne 1 ]; then
			echo "${scenario} must emit exactly one passing observation for ${check}" >&2
			exit 1
		fi
	done < <(printf '%s\n' "${checks}" | tr ',' '\n')
done < "${REPO_ROOT}/qualification/required-checks.tsv"

{
	printf 'source_commit=%s\n' "${SOURCE_COMMIT}"
	printf 'environment_id=%s\n' "${ENVIRONMENT_ID}"
	printf 'control_plane_hosts=%s\n' "${CONTROL_PLANE_HOSTS}"
	printf 'worker_hosts=%s\n' "${WORKER_HOSTS}"
	printf 'qualification_completed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "${OUTPUT}/manifest.txt"
echo "ENVIRONMENT QUALIFICATION GREEN: unsigned observations written to ${OUTPUT} for independent review"
