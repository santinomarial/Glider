# Production hardening

Use a dedicated three-member etcd cluster on separate failure domains, enable
etcd peer and client TLS, and restrict its network policy to Glider identities.
Run at least three control-plane replicas behind a health-checked TCP load
balancer. Never enable `--insecure-development` outside an isolated test.

Issue short-lived certificates from an external CA, use distinct operator,
node, monitoring, and etcd-client roles, and keep private keys and the Glider
secret key readable only by their service users. Rotate leaf certificates
before one third of their validity remains. Store the release signing key and
backup encryption key outside cluster nodes.

Keep the packaged systemd sandbox controls intact. Permit only the documented
kernel capabilities for `gliderd`; do not grant them to the control plane or
CLI. Restrict API and metrics listeners with host firewalls, ship JSON logs to
append-only storage, and alert on authorization failures and leadership churn.

Apply OS security updates, pin Glider artifacts by signature and checksum, and
run `glider-admin preflight` before enabling services or after kernel changes.
