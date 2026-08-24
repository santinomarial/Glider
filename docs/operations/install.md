# Installation and host integration

Release archives contain statically linked Linux binaries for amd64 and arm64,
systemd units, sysusers/tmpfiles declarations, and example environment files.
Verify `SHA256SUMS.sig` against a separately trusted release public key, then
verify every checksum before extracting. Install ordinary binaries in
`/usr/bin`, the privileged `glider-exec` helper in `/usr/libexec/glider`, units
in `/usr/lib/systemd/system`, and declarations in their corresponding
`sysusers.d` and `tmpfiles.d` directories.

The archive's `install.sh install` performs those placements with fixed modes;
`install.sh uninstall` removes only shipped executables, units, sysusers, and
tmpfiles declarations. It deliberately preserves `/etc/glider`, PKI and backup
keys, `/var/lib/glider*`, and operator backups. Packaging qualification runs
both commands inside an isolated root and proves that operator-owned data
survives uninstall.

Copy—not symlink—the example environment file into `/etc/glider`, replace all
`CHANGE_ME` values, install certificates with owner-only private-key modes,
run `systemd-sysusers` and `systemd-tmpfiles --create`, then enable the relevant
service. The control plane runs as the unprivileged `glider` account. The node
agent retains only the capabilities required for namespaces, mounts, cgroups,
and networking and receives write access only to its state paths.

Run `glider-admin config validate --kind=controlplane|node|backup --file=PATH
--check-files` after every configuration or credential change. The validator
parses the systemd environment format without executing it; rejects duplicate,
unknown, placeholder, insecure, or shell-like values; requires the production
identity/TLS/storage arguments; validates CA and leaf PEM, keypair matching,
private modes, and executable helpers. Each packaged service runs the same
check through `ExecStartPre` and fails closed.

For automated recovery points, install `glider-backup.service` and
`glider-backup.timer`, copy `backup.env.example` to `/etc/glider/backup.env`,
generate `/etc/glider/backup.key` with `glider-admin backup-key`, escrow a copy
of that key outside the cluster, then run `systemctl enable --now
glider-backup.timer`. Do not enable the timer until an off-host immutable-copy
job monitors `/var/lib/glider-backup`.

`scripts/release.sh` requires an Ed25519 private key through
`GLIDER_SIGNING_KEY`; it refuses unsigned releases. Builds use `CGO_ENABLED=0`,
`-trimpath`, a fixed source epoch, an empty Go build ID, embedded version data,
sorted archive entries, numeric ownership, checksums, a dependency module
inventory, and detached Ed25519 signatures. `scripts/verify-release.sh` is the
release acceptance check. Every release also contains an SPDX 2.3 dependency
SBOM and an in-toto SLSA v1 provenance statement binding both architecture
archives to the exact Git commit and SHA-256 digest. Verification fails if an
artifact no longer matches its provenance subject. Archive normalization uses
local GNU tar when available and the pinned `golang:1.26.6` container otherwise,
so the same release command works on macOS.
