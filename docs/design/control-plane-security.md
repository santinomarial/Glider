# Control-plane transport security

Production control-plane and CLI traffic uses TLS 1.3 with mutual certificate
authentication. The control plane refuses to start without a server
certificate, key, and client CA. The CLI likewise requires a client
certificate, key, trusted CA, and explicit server name. Plaintext operation is
available only through the visibly named `--insecure-development` switch.

Client certificate common names identify principals. Organizational-unit
values assign roles:

| Role | Access |
|---|---|
| `viewer` | list/get resources and events |
| `operator` | viewer access plus task, workload, service, and scheduling mutations |
| `node` | node registration/status and event reporting |
| `admin` | all RPCs |

Authorization is deny-by-default. A verified certificate without a recognized
role is authenticated but cannot call any method. Certificate issuance and
rotation tooling and node operational transport remain separate production
gates. Both `glider-controlplane` and `gliderd` require certificate-authenticated
TLS to etcd unless the explicit `--insecure-etcd` development switch is used.
The metrics listener reuses the control-plane server identity and requires a
client certificate; operators should issue a narrowly scoped monitoring
identity and restrict the listener at the network layer.

Cluster DNS is authoritative UDP within the pod/control network. It is not an
Internet-facing resolver, does not recurse, and must be isolated by host
firewall or network policy. DNSSEC is not a substitute for that boundary.
