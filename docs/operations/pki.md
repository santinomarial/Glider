# Cluster PKI and rotation

Initialize a cluster-specific offline Ed25519 CA with `glider-admin pki init`.
Issue short-lived server and client certificates with `pki issue`; identities
carry a common name, a deny-by-default role organizational unit, and a SPIFFE
URI of `spiffe://glider/<cluster>/<role>/<name>`. Private files are
create-exclusive mode 0600 and leaf validity is capped at 397 days.

For routine rotation, issue a replacement to new paths, deploy it, restart or
reload the consumer, verify connections, then retire the old files. For CA
rotation, create a new offline CA and use `pki bundle` to build a validated
old+new trust bundle. Deploy the overlap bundle to every verifier, rotate every
leaf to the new CA, verify the fleet, then deploy a new-only bundle. Never
replace a trust root and leaf certificates in one unobservable step.

The CA private key must not be installed on control-plane or node hosts. Glider
does not provide an online CA or revocation service; operators needing those
features should integrate their existing certificate manager and preserve the
same role and identity contract.
