#!/usr/bin/env bash
set -euo pipefail
DIR="${1:-dist}"
openssl pkeyutl -verify -pubin -inkey "${DIR}/release-public-key.pem" -rawin -in "${DIR}/SHA256SUMS" -sigfile "${DIR}/SHA256SUMS.sig"
(cd "${DIR}" && sha256sum --check SHA256SUMS)
go run ./tools/release-metadata verify --dir "${DIR}"
for archive in "${DIR}"/*.tar.gz; do
	tar -tzf "${archive}" | grep '/bin/glider$' >/dev/null
	tar -tzf "${archive}" | grep '/bin/gliderd$' >/dev/null
	tar -tzf "${archive}" | grep '/libexec/glider-exec$' >/dev/null
	tar -tzf "${archive}" | grep '/install.sh$' >/dev/null
	tar -tzf "${archive}" | grep '/monitoring/glider.rules.yml$' >/dev/null
	tar -tzf "${archive}" | grep '/monitoring/glider-dashboard.json$' >/dev/null
	stage="$(mktemp -d)"
	root="$(mktemp -d)"
	tar -xzf "${archive}" -C "${stage}"
	package="$(find "${stage}" -mindepth 1 -maxdepth 1 -type d -name 'glider-*' -print -quit)"
	"${package}/install.sh" install --root "${root}"
	test -x "${root}/usr/bin/glider-controlplane"
	test -x "${root}/usr/libexec/glider/glider-exec"
	test -f "${root}/usr/lib/systemd/system/glider-backup.timer"
	test -f "${root}/usr/share/glider/monitoring/glider.rules.yml"
	test -f "${root}/usr/share/glider/monitoring/glider-dashboard.json"
	test -f "${root}/etc/glider/controlplane.env.example"
	touch "${root}/etc/glider/operator-owned.conf"
	"${package}/install.sh" uninstall --root "${root}"
	test ! -e "${root}/usr/bin/glider-controlplane"
	test ! -e "${root}/usr/share/glider/monitoring/glider-dashboard.json"
	test -f "${root}/etc/glider/operator-owned.conf"
done
echo "release signatures, checksums, and archive layouts verified"
