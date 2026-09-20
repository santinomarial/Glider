# Changelog

This file records user-facing changes. Entries under Unreleased describe the
development branch and do not imply a published or certified release.

## Unreleased — 0.2.0-dev

### Documentation

- Redesigned architecture diagrams with semantic colors, focused views,
  accessible descriptions, and SVG/PNG exports through `make diagrams`.
- Corrected worker-to-etcd routing, backup ownership, concurrent lease-loss
  behavior, and the explicit `DELETING` lifecycle state in diagrams.
- Restructured the README around the local demo, architecture, verification,
  and explicit deployment limits.
- Added a guided control-plane demo and a portfolio/presentation recording guide.
- Added contribution, support, conduct, and roadmap guidance.
- Licensed the project under MIT.

### Repository automation

- Added public CI for build, vet, unprivileged race tests, API policy, the demo,
  and documentation rendering.
- Added issue forms, a pull request template, and weekly dependency updates.
- Included the MIT license in release archives and installed packages.
- Fixed read-only image fixture cleanup for tests running without root, while
  explicitly checking that published layers remain read-only.

### Existing development work

The development branch includes typed v2 API and CLI support, packaged HA and
upgrade qualification, and portable signed release tooling. The
[compatibility matrix](docs/release/compatibility-matrix.md) and
[readiness contract](docs/release/production-readiness.md) describe the current
boundaries. This section summarizes existing work, not newly released features.

## Historical milestone — v0.1.0

See the [v0.1.0 audit](docs/release/v0.1.0.md) for the original educational
milestone and its limitations. Later development supersedes some of those
limitations; the audit remains a record of that milestone.
