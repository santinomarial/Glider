# Security test gate

`scripts/test-security.sh` runs Glider's adversarial security suite in a
privileged Linux environment. It verifies OCI digest and size integrity,
archive and snapshot path-traversal rejection, namespace and root filesystem
isolation, input rejection, capability reduction, `no_new_privs`, and seccomp
denial of namespace-creation syscalls. On macOS it uses Docker Desktop's Linux
VM and the native machine architecture.

This gate complements the threat model in `docs/design/security-model.md`.
Passing it is required for a release; it is not a claim that kernel isolation
eliminates every container escape class.
