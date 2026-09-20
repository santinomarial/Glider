# Contributing to Glider

Start with the [README](README.md), [architecture](docs/architecture/overview.md),
and [roadmap](ROADMAP.md). For a substantial API, runtime, or security change,
open an issue explaining the problem and proposed contract before implementing
it. Small documentation fixes can go straight to a pull request.

## Development setup

Use Go 1.26.6 or newer, Git, Bash, and Make. Docker is required for documentation
diagram rendering and the portable Linux verification environment. Real
runtime tests require privileged Linux with cgroup v2; use a disposable VM
or development environment.

```bash
git clone https://github.com/santinomarial/Glider.git
cd Glider
make demo
make docs
```

Glider's runtime targets Linux. A successful demo on macOS checks control-plane
behavior, not Linux process isolation. See the
[demo tutorial](docs/tutorials/local-demo.md) for the exact boundary.

## Making a change

1. Create a branch with a descriptive name.
2. Read the affected subsystem's design contract and ADRs.
3. Implement the smallest coherent change. Preserve unrelated work.
4. Add regression coverage when behavior or a safety invariant changes.
5. Format changed Go files with `gofmt` and run relevant checks.
6. Update affected commands, contracts, or runbooks in the same pull request.

For a Go change, run focused package tests first. Run `make test` for runtime
or broad integration changes. For documentation, run `make docs`. The full
`make production-gate` requires a clean checkout and is the release software
qualification process, not a substitute for focused iteration.

Never weaken test requirements, skip privileged tests while claiming runtime
coverage, or describe local test success as production deployment certification.

## Pull requests

Explain the problem, resulting behavior, and relevant tests. Include any
compatibility, migration, or rollback implications. For invariant changes,
state which failure case the implementation now handles. Keep secrets,
private hostnames, and operator data out of examples and logs.

Documentation follows the [documentation standard](docs/contributing/documentation.md).
Accepted ADRs remain historical records; supersede them with a new ADR when
a decision changes.

## Reporting and conduct

Use [SUPPORT.md](SUPPORT.md) for questions and reproducible bugs. Follow
[SECURITY.md](SECURITY.md) for suspected vulnerabilities. Be respectful and
specific when reviewing work, explain disagreements with evidence, and follow
the [code of conduct](CODE_OF_CONDUCT.md).

## License

Contributions to Glider are provided under the project's [MIT license](LICENSE).
Submit only code and documentation you have the right to contribute. Preserve
applicable third-party copyright notices and license terms.
