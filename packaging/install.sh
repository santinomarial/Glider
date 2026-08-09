#!/usr/bin/env bash
set -euo pipefail

ACTION="${1:-}"
if [ "${ACTION}" != install ] && [ "${ACTION}" != uninstall ]; then
	echo "usage: install.sh install|uninstall [--root ABSOLUTE_PATH]" >&2
	exit 2
fi
shift
ROOT=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--root)
		ROOT="${2:-}"
		shift 2
		;;
	*)
		echo "unknown argument: $1" >&2
		exit 2
		;;
	esac
done
if [ -n "${ROOT}" ] && { [ "${ROOT#/}" = "${ROOT}" ] || [ "${ROOT}" = / ]; }; then
	echo "--root must be an absolute path other than /" >&2
	exit 2
fi
if [ -z "${ROOT}" ] && [ "$(id -u)" -ne 0 ]; then
	echo "installation into / requires root" >&2
	exit 2
fi

SOURCE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
path() { printf '%s%s' "${ROOT}" "$1"; }

if [ "${ACTION}" = install ]; then
	install -d -m 0755 "$(path /usr/bin)" "$(path /usr/libexec/glider)" "$(path /usr/lib/systemd/system)" "$(path /usr/lib/sysusers.d)" "$(path /usr/lib/tmpfiles.d)" "$(path /usr/share/glider/monitoring)" "$(path /etc/glider)"
	for binary in glider glider-admin glider-controlplane gliderd glider-runtime; do
		install -m 0755 "${SOURCE}/bin/${binary}" "$(path /usr/bin/${binary})"
	done
	install -m 0755 "${SOURCE}/libexec/glider-exec" "$(path /usr/libexec/glider/glider-exec)"
	for unit in "${SOURCE}"/systemd/*.service "${SOURCE}"/systemd/*.timer; do
		install -m 0644 "${unit}" "$(path /usr/lib/systemd/system/$(basename "${unit}"))"
	done
	install -m 0644 "${SOURCE}/systemd/glider.sysusers" "$(path /usr/lib/sysusers.d/glider.conf)"
	install -m 0644 "${SOURCE}/systemd/glider.tmpfiles" "$(path /usr/lib/tmpfiles.d/glider.conf)"
	install -m 0644 "${SOURCE}/monitoring/glider.rules.yml" "$(path /usr/share/glider/monitoring/glider.rules.yml)"
	install -m 0644 "${SOURCE}/monitoring/glider-dashboard.json" "$(path /usr/share/glider/monitoring/glider-dashboard.json)"
	for example in "${SOURCE}"/config/*.example; do
		install -m 0640 "${example}" "$(path /etc/glider/$(basename "${example}"))"
	done
	echo "Glider files installed; configure /etc/glider before enabling services."
	exit 0
fi

for binary in glider glider-admin glider-controlplane gliderd glider-runtime; do
	rm -f "$(path /usr/bin/${binary})"
done
rm -f "$(path /usr/libexec/glider/glider-exec)"
for unit in glider-controlplane.service gliderd.service glider-backup.service glider-backup.timer; do
	rm -f "$(path /usr/lib/systemd/system/${unit})"
done
rm -f "$(path /usr/lib/sysusers.d/glider.conf)" "$(path /usr/lib/tmpfiles.d/glider.conf)"
rm -f "$(path /usr/share/glider/monitoring/glider.rules.yml)" "$(path /usr/share/glider/monitoring/glider-dashboard.json)"
echo "Glider executables and units removed; configuration, keys, backups, and runtime data were preserved."
