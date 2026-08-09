#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
PLAN="${WORK}/plan"
mkdir -p "${PLAN}"
{
	printf 'ENVIRONMENT_ID=contract-test\n'
	printf 'CONTROL_PLANE_HOSTS=cp-a.example,cp-b.example,cp-c.example\n'
	printf 'WORKER_HOSTS=worker-a.example,worker-b.example\n'
	printf 'LOAD_BALANCER_ENDPOINT=glider.example:8443\n'
	printf 'OFF_HOST_BACKUP_URI=s3://glider-contract-test/backups\n'
	printf 'MONITORING_RECEIVER=contract-test-oncall\n'
	printf 'CERTIFICATE_MANAGER=contract-test-pki\n'
} > "${PLAN}/plan.env"
while IFS=$'\t' read -r scenario checks; do
	script="${PLAN}/${scenario}"
	{
		printf '#!/usr/bin/env bash\nset -euo pipefail\n'
		while IFS= read -r check; do
			printf "printf 'GLIDER_ASSERT scenario=%s check=%s result=pass observed=fixture\\n'\n" "${scenario}" "${check}"
		done < <(printf '%s\n' "${checks}" | tr ',' '\n')
	} > "${script}"
	chmod 0755 "${script}"
done < "${REPO_ROOT}/qualification/required-checks.tsv"

"${REPO_ROOT}/scripts/qualify-environment.sh" "${PLAN}" "${WORK}/good"
test "$(find "${WORK}/good" -name '*.log' -type f | wc -l | tr -d ' ')" -eq 9
required_count="$(tr '\t' ',' < "${REPO_ROOT}/qualification/required-checks.tsv" | cut -d, -f2- | tr ',' '\n' | sed '/^$/d' | wc -l | tr -d ' ')"
observed_count="$(grep -h -c '^GLIDER_ASSERT ' "${WORK}/good"/*.log | awk '{ total += $1 } END { print total }')"
if [ "${observed_count}" -ne "${required_count}" ]; then
	echo "qualification emitted ${observed_count} assertions, want ${required_count}" >&2
	exit 1
fi
sed '/check=api_through_lb /d' "${PLAN}/multi-host-lb" > "${PLAN}/multi-host-lb.tmp"
mv "${PLAN}/multi-host-lb.tmp" "${PLAN}/multi-host-lb"
chmod 0755 "${PLAN}/multi-host-lb"
if "${REPO_ROOT}/scripts/qualify-environment.sh" "${PLAN}" "${WORK}/missing-assertion" >/dev/null 2>&1; then
	echo "environment qualification accepted a missing required assertion" >&2
	exit 1
fi
printf "printf 'GLIDER_ASSERT scenario=multi-host-lb check=api_through_lb result=pass observed=\\n'\n" >> "${PLAN}/multi-host-lb"
if "${REPO_ROOT}/scripts/qualify-environment.sh" "${PLAN}" "${WORK}/empty-observation" >/dev/null 2>&1; then
	echo "environment qualification accepted an empty observation" >&2
	exit 1
fi
printf "printf 'GLIDER_ASSERT scenario=multi-host-lb check=api_through_lb result=pass observed=first\\n'\nprintf 'GLIDER_ASSERT scenario=multi-host-lb check=api_through_lb result=pass observed=second\\n'\n" >> "${PLAN}/multi-host-lb"
if "${REPO_ROOT}/scripts/qualify-environment.sh" "${PLAN}" "${WORK}/duplicate-observation" >/dev/null 2>&1; then
	echo "environment qualification accepted duplicate passing observations" >&2
	exit 1
fi
echo "ENVIRONMENT QUALIFICATION CONTRACT GREEN: complete plans pass; missing, empty, and duplicate observations fail"
