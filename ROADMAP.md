# Roadmap

This roadmap describes priorities, not delivery dates or completed features.
The [readiness contract](docs/release/production-readiness.md) remains the
authority for production qualification.

## Onboarding and public evidence

- Record a short control-plane demo with clear fixture disclosures.
- Publish a prerelease with verified artifacts and reproducible evidence.
- Complete GitHub repository metadata and a social preview image.

## Deployment qualification

- Exercise multi-host availability and failure recovery on the target topology.
- Collect certificate renewal, node replacement, disk-pressure, backup, and
  monitoring-delivery evidence.
- Run sustained workload and network tests on target hardware.
- Obtain the independent security review required for production certification.

Follow the [environment evidence contract](docs/release/environment-evidence.md)
for completion criteria. Passing a local software gate does not close these
items.

## Operator experience

- Improve the readability and verifiability of the demo's state transitions.
- Extend guided tutorials for real workloads and recovery exercises.
- Evaluate a browser console after defining API exposure, identity, and
  authorization requirements. No browser console is currently implemented.

## Scope

Glider targets Linux and cgroup v2. Kubernetes parity, arbitrary kernel
support, and exactly-once execution during partitions remain outside the
current contract. See the [architecture overview](docs/architecture/overview.md).
