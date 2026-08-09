# Cluster PKI and rotation

Initialize a cluster-specific offline Ed25519 CA with `glider-admin pki init`.
Issue short-lived server and client certificates with `pki issue`; identities
carry a common name, a deny-by-default role organizational unit, and a SPIFFE
URI of `spiffe://glider/<cluster>/<role>/<name>`. Private files are
create-exclusive mode 0600 and leaf validity is capped at 397 days.

For routine rotation, issue a replacement to new paths, deploy it, restart or
reload the consumer, verify connections, then retire the old files. Glider
reloads its server, node/client, and etcd-client leaf keypairs on every new TLS
handshake. An external certificate manager may therefore atomically replace
the configured certificate and key paths without restarting the process. If
the two file renames briefly expose a mismatched pair, Glider retains the last
valid pair until both new files match and are currently valid. Monitor expiry:
an invalid renewal does not extend the old certificate's lifetime.

The certificate manager must preserve the existing cluster SPIFFE identity,
common name, role OU, extended key usage, owner, and modes. Qualify renewal by
maintaining an established connection, rotating the files, opening a new
connection that observes the new serial, and proving that peer and stale-role
certificates remain denied.

For CA
rotation, create a new offline CA and use `pki bundle` to build a validated
old+new trust bundle. Deploy the overlap bundle to every verifier, rotate every
leaf to the new CA, verify the fleet, then deploy a new-only bundle. Never
replace a trust root and leaf certificates in one unobservable step.

The CA private key must not be installed on control-plane or node hosts. Glider
does not provide an online CA or revocation service; automated issuance and
revocation belong to the operator's certificate manager, while Glider provides
safe zero-restart leaf activation and enforces the role/identity contract.
