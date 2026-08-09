# Release-environment evidence

A production tag requires `scripts/production-release.sh EVIDENCE_DIR`. It runs
the complete local gate only after independently signed environment evidence is
verified against the exact source commit. Set
`GLIDER_ENVIRONMENT_EVIDENCE_PUBLIC_KEY` to the trusted reviewer's Ed25519
public key.

The evidence directory must contain non-empty results named:

- `multi-host-lb.log`: three control-plane hosts behind the production load
  balancer, including controller and etcd member loss recovery times.
- `off-host-restore.log`: scheduled immutable-copy proof and isolated restore.
- `rolling-upgrade.log`: mixed-version canary, migration, and rollback result.
- `node-replacement.log`: external certificate renewal and full replacement.
- `disk-pressure.log`: sustained pressure, cordon, evacuation, and recovery.
- `network-qualification.log`: cross-host VXLAN, VIP, DNS, ingress, and egress.
- `monitoring-delivery.log`: scrape, dashboard, rule, routing, and pager drill.
- `security-review.md`: independent threat-model findings and disposition.

`manifest.txt` must include the exact `source_commit`, a non-empty
`independent_reviewer`, and `security_decision=approved`. Hash every evidence
file except `SHA256SUMS` and `SHA256SUMS.sig` into GNU-format `SHA256SUMS`, then
sign that checksum file with the reviewer's Ed25519 key:

```sh
openssl pkeyutl -sign -rawin -inkey reviewer.key \
  -in SHA256SUMS -out SHA256SUMS.sig
```

The verifier rejects missing, empty, unsigned, altered, stale-commit, or
non-approved evidence. It deliberately provides no flag to waive a P0 gate.
