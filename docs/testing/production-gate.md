# Production release gate

Run `make production-gate` from a clean checkout with Docker available. The
gate fails on formatting, build, vet, unit/race, privileged runtime, isolation,
fuzz, repeated convergence, monitoring, encrypted recovery, secret handling,
packaged HA, packaged upgrade, benchmark, release-signature, checksum, SBOM,
provenance, installation, or uninstallation failure.

The default stress profile repeats the privileged runtime suite 20 times,
admission fuzzing for 60 seconds, and convergence chaos 50 times. The resulting
`dist/evidence` directory contains the exact commit, tool images, compatibility
matrix, individual logs, and hashes for audit or release attachment. CI may
increase the three duration variables but must not reduce them for a production
tag.

This local gate does not fabricate deployment evidence. Multi-host traffic,
external certificate renewal, off-host backups, monitoring delivery, and
rolling load-balancer behavior must be captured from the release environment.
