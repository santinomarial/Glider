# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private
security-advisory reporting flow for `santinomarial/Glider`. Include affected
versions, deployment assumptions, reproduction steps, impact, and any proposed
mitigation. Do not include production secrets or customer data.

The project acknowledges a complete report within three business days, assigns
severity and an owner within seven days, and provides a remediation plan for a
confirmed issue. Target patch timelines are 7 days for critical, 30 days for
high, 90 days for medium, and the next planned release for low severity. Active
exploitation or a control-plane authentication bypass triggers the incident
process immediately and may require an out-of-band release.

## Supported versions

Until the first stable release, only the newest tagged release is supported.
After 1.0, the newest minor release and the immediately preceding minor release
receive security fixes. Operators must run Glider only on the documented Linux
and cgroup-v2 envelope; unsupported kernels or locally weakened systemd/PKI
settings are outside the security boundary.

## Release and disclosure process

Every release candidate must pass `make security` and `make vulnerability`.
The latter pins the official Go `govulncheck` scanner and fails on known
vulnerable functions reachable from production or test code. The release also
publishes a signed checksum manifest, SPDX SBOM, and SLSA provenance statement.

Confirmed fixes receive regression tests where safe. Advisories identify
affected versions, severity, impact, mitigations, fixed versions, and credit.
Coordinated public disclosure occurs after supported fixes are available, or
earlier when exploitation is public and immediate defensive guidance is more
important than embargo. Scanner findings cannot be silently waived: an
exception requires a public, time-bounded risk record approved by two
maintainers and linked from the release evidence.
