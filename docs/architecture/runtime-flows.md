# Runtime and reconciliation flows

These dynamic views show how Glider turns declared intent into a running
container and how it withdraws stale node authority. They emphasize durable
commit points and failure handling rather than implementation call stacks.

## Workload creation and scheduling

```mermaid
sequenceDiagram
    autonumber
    actor Operator
    participant CLI as glider CLI
    participant API as Control-plane API
    participant ETCD as etcd
    participant WC as Workload controller
    participant SCH as Scheduler
    participant AG as gliderd

    Operator->>CLI: Apply workload specification
    CLI->>API: PutWorkload (mTLS, idempotency key)
    API->>API: Authenticate, authorize, validate, quota check
    API->>ETCD: Transactionally persist desired workload
    ETCD-->>WC: Workload watch event
    WC->>ETCD: Reconcile replica slots into pending Tasks
    ETCD-->>SCH: Pending Task watch event
    SCH->>ETCD: Read eligible Nodes and current reservations
    SCH->>SCH: Filter, score, select Node
    SCH->>ETCD: CAS Task generation + Assignment + reservation
    alt comparison succeeds
        ETCD-->>AG: Assignment watch event
        AG->>AG: Verify lease and current generation
        AG->>AG: Reconcile image, snapshot, network, cgroup, process
        AG->>ETCD: Generation-fenced observed RUNNING status
    else comparison fails
        ETCD-->>SCH: Conflict
        SCH->>SCH: Re-read and retry with bounded backoff
    end
```

The first externally meaningful commit is the API transaction that stores
desired state. The placement commit is the scheduler CAS: task generation,
assignment, and resource reservation change atomically. `gliderd` reports
observation only; it cannot make its local process authoritative.

## Cold OCI image to running process

```mermaid
sequenceDiagram
    autonumber
    participant AG as gliderd reconciler
    participant REG as OCI registry
    participant CS as Content store
    participant UP as Layer unpacker
    participant OV as OverlayFS snapshotter
    participant RT as Runtime launcher
    participant K as Linux kernel
    participant P as Workload process

    AG->>REG: Resolve manifest for platform
    REG-->>AG: Manifest, config, layer descriptors
    loop Each missing descriptor
        AG->>REG: Download blob
        REG-->>AG: Blob bytes
        AG->>CS: Verify digest and atomically publish
    end
    AG->>UP: Extract verified layers
    UP->>UP: Reject traversal, special-file, and limit violations
    UP-->>AG: Immutable layer directories
    AG->>OV: Create per-attempt lower/upper/work/merged snapshot
    OV->>K: Mount OverlayFS
    AG->>RT: Launch with rootfs, limits, identity, generation
    RT->>K: Create namespaces and cgroup v2 membership
    RT->>K: pivot_root, capabilities, no_new_privs, seccomp
    RT->>P: Start workload under Glider PID 1 supervisor
    P-->>RT: Exit status or signal
    RT-->>AG: Durable terminal result after cleanup
```

The content store publishes a blob only after its declared digest matches.
Layer extraction occurs into a temporary directory and becomes visible only
after success. Runtime state is written before host-visible resources so crash
recovery can identify and remove partial work.

## Lease loss and generation fencing

```mermaid
sequenceDiagram
    autonumber
    participant OLD as Partitioned worker
    participant ETCD as etcd quorum
    participant NC as Node controller
    participant SCH as Scheduler
    participant NEW as Replacement worker

    OLD-xETCD: Lease keepalive fails
    ETCD-->>NC: Lease expiration event
    NC->>ETCD: Mark Node unreachable and fence generation N
    NC->>ETCD: Return Task to pending
    ETCD-->>SCH: Pending Task watch event
    SCH->>ETCD: CAS bind generation N+1 to replacement Node
    ETCD-->>NEW: Assignment generation N+1
    NEW->>ETCD: Confirm authority and reconcile
    OLD->>OLD: Uncertainty deadline expires, then stop generation N workloads
    OLD->>ETCD: Attempt stale generation N status
    ETCD-->>OLD: Reject stale generation
```

Glider does not claim instantaneous exactly-once execution across a partition.
Safety comes from monotonic generations, rejection of stale writes, and the
old node's bounded self-fencing deadline. See the
[failure model](../design/failure-model.md) for the precise guarantee.

## Local container state machine

```mermaid
stateDiagram-v2
    [*] --> CREATING
    CREATING --> CREATED: namespaces, rootfs, cgroup prepared
    CREATED --> RUNNING: workload exec confirmed
    RUNNING --> STOPPING: desired stop or termination signal
    RUNNING --> EXITED: workload exits
    STOPPING --> EXITED: graceful exit or SIGKILL escalation
    CREATING --> FAILED: setup error or recovery
    CREATED --> FAILED: exec failure or recovery
    RUNNING --> FAILED: invalid identity or unrecoverable inconsistency
    EXITED --> [*]: idempotent cleanup complete
    FAILED --> [*]: idempotent cleanup complete
```

`RUNNING` is valid only while the recorded process identity, namespace root,
cgroup membership, and assignment generation match. The authoritative local
transition contract is [container lifecycle](../design/container-lifecycle.md).

## Failure checkpoints

| Checkpoint | Durable evidence | Safe retry behavior |
|---|---|---|
| Before assignment CAS | Pending task | Any scheduler replica may retry |
| After assignment CAS, before node action | Assignment generation and reservation | Assigned node reconciles; scheduler does not duplicate bind |
| During image download | Temporary content file | Verify and publish, or discard partial file |
| During layer extraction | Temporary layer directory | Re-extract; immutable final layer is absent until success |
| During runtime creation | Container state record precedes resources | Recovery cleans known partial resources |
| After workload start | PID identity, start time, cgroup, generation | Reconcile verifies all evidence before retaining RUNNING |
| During termination | STOPPING/terminal record plus kernel state | Repeated stop and cleanup are idempotent |

Related: [container lifecycle](../design/container-lifecycle.md),
[image store](../design/image-store.md),
[leases and reconciliation](../design/leases-overlay-workloads-health.md), and
[ADR 0006](../adr/0006-glider-init-pid1-supervisor.md).
