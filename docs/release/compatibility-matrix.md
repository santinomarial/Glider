# Compatibility matrix

This matrix defines the production envelope tested by `make production-gate`.

| Component | Supported production range | Qualification image or mechanism |
|---|---|---|
| Linux | cgroup v2, namespaces, OverlayFS, nftables, VXLAN | Privileged Linux container on the host kernel |
| CPU | amd64 and arm64 | Reproducible static release builds for both architectures |
| Glider schema | Current version and the immediately previous version | Packaged TLS-etcd upgrade, rollback, and canary test |
| Glider API | Typed `glider.v2` used by the bundled CLI and node agent; Struct-based `glider.v1` compatibility service | Immutable-baseline breaking check, reproducible generation, typed-wire CLI and mTLS generation-fencing tests, and packaged mixed v1/v2 mutation qualification |
| etcd | Embedded etcd version selected by `go.mod` | Three-member quorum-loss and encrypted backup/restore tests |
| Prometheus | Prometheus 3.7 rule syntax | Pinned `promtool` validation and alert evaluation |
| Init system | systemd with sysusers and tmpfiles | Packaged units, preflight, installer, and uninstaller tests |

The kernel feature preflight remains authoritative on a target host. A release
outside this envelope requires a separate qualification and an updated matrix.
