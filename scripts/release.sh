#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")"
OUTPUT="${REPO_ROOT}/dist"
SOURCE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "${REPO_ROOT}" log -1 --format=%ct)}"
SIGNING_KEY="${GLIDER_SIGNING_KEY:-}"
if [ -z "${SIGNING_KEY}" ] || [ ! -f "${SIGNING_KEY}" ]; then
	echo "GLIDER_SIGNING_KEY must name an Ed25519 private key" >&2
	exit 2
fi
command -v go >/dev/null
command -v openssl >/dev/null
command -v sha256sum >/dev/null
rm -rf "${OUTPUT}"
mkdir -p "${OUTPUT}"
STAGE_ROOT="$(mktemp -d)"
trap 'rm -rf "${STAGE_ROOT}"' EXIT
export CGO_ENABLED=0 SOURCE_DATE_EPOCH="${SOURCE_EPOCH}"
LDFLAGS="-s -w -buildid= -X github.com/santinomarial/glider/internal/version.Version=${VERSION}"
BINARIES=(glider glider-admin glider-controlplane gliderd glider-runtime glider-exec)
for arch in amd64 arm64; do
	stage="${STAGE_ROOT}/glider-${VERSION}-linux-${arch}"
	mkdir -p "${stage}/bin" "${stage}/libexec" "${stage}/systemd" "${stage}/config"
	for binary in "${BINARIES[@]}"; do
		target="${stage}/bin/${binary}"
		if [ "${binary}" = glider-exec ]; then target="${stage}/libexec/${binary}"; fi
		GOOS=linux GOARCH="${arch}" go build -trimpath -buildvcs=true -ldflags "${LDFLAGS}" -o "${target}" "./cmd/${binary}"
	done
	cp packaging/systemd/* "${stage}/systemd/"
	cp packaging/config/* "${stage}/config/"
	cp VERSION README.md "${stage}/"
	archive="${OUTPUT}/glider-${VERSION}-linux-${arch}.tar.gz"
	tar --sort=name --mtime="@${SOURCE_EPOCH}" --owner=0 --group=0 --numeric-owner -C "${STAGE_ROOT}" -czf "${archive}" "$(basename "${stage}")"
	rm -rf "${stage}"
done
(cd "${OUTPUT}" && sha256sum *.tar.gz > SHA256SUMS)
go list -m -json all > "${OUTPUT}/modules.json"
openssl pkeyutl -sign -rawin -inkey "${SIGNING_KEY}" -in "${OUTPUT}/SHA256SUMS" -out "${OUTPUT}/SHA256SUMS.sig"
openssl pkey -in "${SIGNING_KEY}" -pubout -out "${OUTPUT}/release-public-key.pem"
echo "release ${VERSION} created in ${OUTPUT}"
