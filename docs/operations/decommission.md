# Cluster decommission

1. Freeze new workload admission and take a final encrypted, authenticated,
   off-host snapshot. Verify it by restoring into an isolated environment.
2. Cordon and drain each worker, confirm its lease is absent, then use the safe
   node-removal operation. Confirm no assignments or reservations remain.
3. Stop control-plane replicas one at a time, then stop etcd after exporting its
   final member and revision inventory. Disable backup timers and alert routes.
4. Run the package uninstaller. It intentionally preserves configuration,
   credentials, backups, and runtime state for explicit disposition.
5. Revoke all cluster certificates and delete private keys, secret-encryption
   keys, runtime data, and local image content according to the retention and
   media-sanitization policy. Destruction must require a second operator.
6. Preserve required audit logs and release evidence, remove DNS/load-balancer
   records and firewall rules, and document who verified each disposed asset.
