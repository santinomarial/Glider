# Container view

This C4-style container view shows Glider's deployable processes and durable
stores. It answers ownership and protocol questions; package-level mechanics
remain in the linked design documents.

## Deployable architecture

```mermaid
flowchart TB
    cli["glider CLI<br/>Operator client"]
    admin["glider-admin<br/>PKI, backup, migration, recovery"]

    subgraph cp["Control-plane host set"]
        api["glider-controlplane<br/>mTLS gRPC API, admission, RBAC"]
        controllers["Controllers<br/>Workloads, services, nodes, rollouts"]
        scheduler["Scheduler<br/>Filter, score, CAS bind"]
    end

    etcd[("etcd cluster<br/>Authoritative desired state, status,<br/>leases, idempotency, events")]

    subgraph worker["Each worker node"]
        agent["gliderd<br/>Assignment watch and reconciliation"]
        runtime["Glider runtime<br/>Namespaces, cgroup v2, security, PID 1"]
        image["Image pipeline<br/>Pull, verify, unpack, OverlayFS"]
        network["Network agent<br/>IPAM, veth, bridge, VXLAN, policy"]
        local[("Node-local state<br/>Containers, content, layers, snapshots")]
        workloads["Workload containers"]
    end

    registry["OCI registry"]

    cli -->|"mTLS gRPC"| api
    admin -->|"mTLS gRPC / authenticated snapshot operations"| api
    api -->|"linearizable reads and transactions"| etcd
    controllers -.->|"watches"| etcd
    controllers -->|"desired-state updates"| etcd
    scheduler -.->|"pending-task and node watches"| etcd
    scheduler -->|"CAS assignment + reservation"| etcd
    agent -.->|"assignment watch and lease keepalive"| etcd
    agent -->|"generation-fenced observed status"| etcd
    agent --> runtime
    agent --> image
    agent --> network
    image -->|"HTTPS OCI Distribution API"| registry
    image --> local
    runtime --> local
    network --> local
    runtime --> workloads
    network --> workloads
```

Dashed arrows are long-lived watches or keepalives. Solid arrows are commands,
transactions, or data writes. `gliderd` does not accept imperative “run this
now” instructions from the control plane; it converges node state from durable
assignments.

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
