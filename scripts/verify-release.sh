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
done
echo "release signatures, checksums, and archive layouts verified"
