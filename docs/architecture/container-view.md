# Container view

This C4-style container view shows Glider's deployable processes and durable
stores. It answers ownership and protocol questions; package-level mechanics
remain in the linked design documents.

## Control plane and clients

This view shows executable processes and their network connections.
All API replicas accept operator requests; only the elected replica runs the
mutating controller loops. Workers maintain direct etcd sessions.

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff","clusterBkg":"#f8fafc","clusterBorder":"#cbd5e1"},"flowchart":{"curve":"basis","nodeSpacing":35,"rankSpacing":50}}}%%
flowchart TB
    accTitle: Glider processes — API, elected controllers, state and workers
    accDescr: CLI calls the API. Controllers and workers transact with etcd. The admin utility connects directly to etcd for backup and migration.
    cli(["glider<br/>Go process · operator CLI"]):::person
    api["glider-controlplane<br/>Go process · API on every replica<br/>Leader-only controllers + scheduler"]:::control
    admin(["glider-admin<br/>Go process · backup / schema tools"]):::person
    store[("etcd cluster<br/>Durable intent, assignments,<br/>status, leases and events")]:::store
    node["gliderd<br/>Go process · one per Linux worker"]:::worker
    cli -->|"Operator requests · mTLS gRPC"| api
    api -->|"Read / watch / transact<br/>etcd mTLS"| store
    admin -->|"Snapshot / schema · etcd mTLS"| store
    node -->|"Watch / lease / status · etcd mTLS"| store
    node -->|"Assignment-scoped secrets · mTLS gRPC"| api
    classDef person fill:#172b4d,stroke:#172b4d,color:#ffffff
    classDef control fill:#e8f0ff,stroke:#3563a4,color:#183b6a
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
    classDef worker fill:#e5f5f0,stroke:#23836b,color:#155b49
```

**Key:** dark = operator tools; blue = control-plane process; purple cylinder
= durable store; green = worker process. Arrows show the client initiating the
labeled connection; watch events return on that connection.
API admission, controllers, and the scheduler share one
process, not separate microservices. PKI issuance and offline restore are omitted.

## Inside a Linux worker

This component view expands `gliderd` and its runtime helpers. The node's own
state is recovery evidence; it cannot replace current etcd assignment authority.

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff","clusterBkg":"#f8fafc","clusterBorder":"#cbd5e1"},"flowchart":{"curve":"basis","nodeSpacing":30,"rankSpacing":45}}}%%
flowchart TB
    accTitle: Linux worker — reconciliation and local resource ownership
    accDescr: The reconciler coordinates image snapshots, networking and process isolation. Runtime records and image data persist locally in separate stores.
    subgraph host["HOST BOUNDARY · Linux + cgroup v2"]
        agent["gliderd reconciler<br/>Verify assignment + lease; ensure desired state"]:::worker
        image["Image pipeline<br/>Pull · verify · unpack · snapshot"]:::worker
        network["Network driver<br/>IPAM · veth · bridge · VXLAN"]:::worker
        runtime["Runtime + PID 1 supervisor<br/>Namespaces · cgroups · seccomp"]:::worker
        images[("Image store<br/>Verified layers + snapshots")]:::store
        records[("Runtime records<br/>Durable lifecycle + identity")]:::store
        process["Workload processes<br/>Isolated rootfs, resources and network"]:::worker
        agent -->|"Ensure rootfs"| image
        agent -->|"Ensure connectivity"| network
        agent -->|"Ensure process"| runtime
        image -->|"Publish verified data"| images
        runtime -->|"Persist lifecycle"| records
        runtime -->|"Launch + supervise"| process
        network -->|"Attach netns + policy"| process
    end
    classDef worker fill:#e5f5f0,stroke:#23836b,color:#155b49
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
```

**Key:** green = node execution; purple cylinder = persisted local data.
Solid arrows are local calls/writes. Both stores are node-local disk.
Image, network, and runtime boxes group implementation concerns,
not independent network services. Registry traffic is shown in the
[cold image flow](runtime-flows.md#cold-oci-image-to-running-process).
Authenticated node operations are described
in [their own contract](../design/node-operations.md).

## Ownership matrix

| Concern | Authoritative owner | Persistence |
|---|---|---|
| API authentication and authorization | `glider-controlplane` | Policy/config plus audit events |
| Desired workloads and services | Control-plane API and controllers | etcd |
| Task placement and generation | Scheduler CAS transaction | etcd |
| Node authority | Lease controller plus node self-fencing | etcd lease and node-local deadline |
| Container realization | `gliderd` reconciler | Node-local state, reported to etcd |
| Process and resource isolation | Glider runtime | Linux kernel plus crash-recovery record |
| Image identity | Digest-resolved image pipeline | Content-addressed node-local store |
| Service endpoints | Service controller from Ready task status | etcd; consumed by discovery/dataplane |
| Secrets at rest | Control plane with external master key material | Encrypted etcd values |

## Key invariants

1. etcd is the cluster source of truth; no process-local cache can create
   assignment authority.
2. Binding a task and reserving capacity is one compare-and-swap transaction.
3. A node acts only while its lease and assignment generation are current.
4. Reconciliation is idempotent: repeating an `Ensure*` operation does not
   create duplicate resources.
5. Desired state and observed state are stored separately; observed status
   cannot mutate intent.

Related: [runtime design](../design/runtime.md),
[image pipeline](../design/image-store.md),
[network/control-plane design](../design/networking-control-plane.md), and
[leases and reconciliation](../design/leases-overlay-workloads-health.md).
