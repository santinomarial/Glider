# Runtime and reconciliation flows

These dynamic views show how Glider turns declared intent into a running
container and how it withdraws stale node authority. They emphasize durable
commit points and failure handling rather than implementation call stacks.

## Workload creation and scheduling

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","actorBkg":"#e8f0ff","actorBorder":"#3563a4","actorTextColor":"#183b6a","signalColor":"#64748b","signalTextColor":"#172b4d","noteBkgColor":"#fff4db","noteBorderColor":"#b9892a","noteTextColor":"#634615"},"sequence":{"mirrorActors":false,"messageMargin":26}}}%%
sequenceDiagram
    accTitle: Workload scheduling — two durable commit points
    accDescr: The API first commits intent. Controllers create tasks and atomically bind them with a resource reservation. The node watches the assignment and reports observed state.
    autonumber
    actor CLI as Operator / CLI
    participant API as API replica
    participant ETCD as etcd
    participant CTRL as Leader controllers
    participant AG as Worker / gliderd

    rect rgb(232, 240, 255)
        Note over CLI,ETCD: 1 · Accept durable intent
        CLI->>API: PutWorkload · mTLS gRPC
        API->>API: Authenticate, authorize, validate
        API->>ETCD: Transaction · desired workload
        ETCD-->>API: Commit confirmed
        API-->>CLI: Accepted, not yet running
    end
    rect rgb(241, 237, 255)
        Note over ETCD,CTRL: 2 · Commit placement
        ETCD-->>CTRL: Workload / task watch events
        CTRL->>ETCD: Create replica tasks and read capacity
        CTRL->>CTRL: Scheduler selects eligible node
        CTRL->>ETCD: CAS · generation + assignment + reservation
    end
    alt comparison succeeds
        rect rgb(229, 245, 240)
        Note over ETCD,AG: 3 · Realize and observe
        ETCD-->>AG: Assignment watch event
        AG->>AG: Check lease and ensure local resources
        AG->>ETCD: Generation-fenced RUNNING status
        end
    else comparison fails
        ETCD-->>CTRL: Conflict · re-read before retry
    end
```

**Key:** numbered solid messages = calls/writes; dashed messages = replies or
watch events. Shaded bands label admission, placement, and execution. All etcd
connections use mTLS in production. Leader controllers combine the workload
controller and scheduler into one participant to keep the sequence readable.

The first externally meaningful commit is the API transaction that stores
desired state. The placement commit is the scheduler CAS: task generation,
assignment, and resource reservation change atomically. `gliderd` reports
observation only; it cannot make its local process authoritative.

## Cold OCI image to running process

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff","clusterBkg":"#f8fafc","clusterBorder":"#cbd5e1"},"flowchart":{"curve":"basis","rankSpacing":40}}}%%
flowchart LR
    accTitle: Cold image path — verify, prepare, isolate, execute
    accDescr: Verified OCI blobs become immutable layers and an OverlayFS rootfs. The runtime persists creation intent, prepares Linux isolation, then confirms workload exec under a PID 1 supervisor.
    subgraph image["01 · VERIFY IMAGE"]
        direction TB
        registry["OCI registry<br/>Manifest + blobs"]:::external
        digest["Verify digest<br/>Atomically publish content"]:::store
        layers["Safely extract layers<br/>Check paths + limits"]:::store
        registry -->|"HTTPS · OCI API"| digest
        digest -->|"Verified bytes"| layers
    end
    subgraph setup["02 · PREPARE ATTEMPT"]
        direction TB
        rootfs["OverlayFS snapshot<br/>Private writable rootfs"]:::worker
        intent[("Persist CREATING record<br/>Before runtime resources")]:::store
        rootfs -->|"Launch specification"| intent
    end
    subgraph execute["03 · ISOLATE AND RUN"]
        direction TB
        isolate["Linux isolation + network<br/>Namespaces · cgroups · veth<br/>Rootfs · capabilities · seccomp"]:::worker
        process["PID 1 supervises workload<br/>Confirm exec → RUNNING"]:::worker
        isolate -->|"CREATED · init recorded"| process
    end
    image -->|"Verified layers"| setup
    setup -->|"Durable identity"| execute
    classDef external fill:#f1f4f8,stroke:#78879c,color:#334155
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
    classDef worker fill:#e5f5f0,stroke:#23836b,color:#155b49
```

**Key:** gray = remote source; purple = verified/persisted data; green = node
execution. Arrows show successful artifact or control handoffs, not separate
network services. The first edge transfers registry data; later edges are local.
The phase groups describe order, not host boundaries. Failure at any stage
prevents progression; the checkpoints below describe cleanup and retry.

The content store publishes a blob only after its declared digest matches.
Layer extraction occurs into a temporary directory and becomes visible only
after success. Runtime state is written before cgroup and process setup so
crash recovery can identify partial execution. Image preparation precedes the
runtime record.

## Lease loss and generation fencing

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","actorBkg":"#e8f0ff","actorBorder":"#3563a4","actorTextColor":"#183b6a","signalColor":"#64748b","signalTextColor":"#172b4d","noteBkgColor":"#fff4db","noteBorderColor":"#b9892a","noteTextColor":"#634615"},"sequence":{"mirrorActors":false,"messageMargin":26}}}%%
sequenceDiagram
    accTitle: Partition recovery — independent fencing and reassignment
    accDescr: An old worker stops after its local uncertainty deadline while the quorum side expires its lease and assigns a newer generation. These clocks are independent. Stale writes are rejected.
    autonumber
    participant OLD as Partitioned worker
    participant ETCD as etcd quorum
    participant CTRL as Leader controllers
    participant NEW as Replacement worker

    OLD-xETCD: Lease keepalive fails
    Note over OLD,NEW: Local self-fencing and quorum recovery have independent timing
    par Local worker deadline
        OLD->>OLD: Deadline expires: stop old workloads
    and Quorum-side recovery
        ETCD-->>CTRL: Lease absent / expiration observed
        CTRL->>ETCD: Evict unreachable node and requeue tasks
        CTRL->>ETCD: CAS assignment with newer generation
        ETCD-->>NEW: New assignment watch event
        NEW->>NEW: Verify authority and reconcile resources
    end
    OLD->>ETCD: After reconnection: stale status write
    ETCD-->>OLD: Reject stale generation
```

**Key:** solid messages = calls/actions; dashed = events/replies; crossed arrow
= failed connection. The `par` lanes show independent progress, not an ordering
guarantee between old-worker shutdown and replacement start. etcd uses mTLS.

Glider does not claim instantaneous exactly-once execution across a partition.
Safety comes from monotonic generations, rejection of stale writes, and the
old node's bounded self-fencing deadline. See the
[failure model](../design/failure-model.md) for the precise guarantee.

## Local container state machine

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff"}}}%%
stateDiagram-v2
    direction TB
    accTitle: Local container lifecycle — terminal states still need deletion
    accDescr: Creation leads to running and then exited or failed. Both terminal states enter deleting. The record disappears only after resource cleanup completes.
    [*] --> CREATING
    CREATING --> CREATED: init identity recorded
    CREATED --> RUNNING: exec confirmed
    RUNNING --> STOPPING: stop requested
    RUNNING --> EXITED: exit observed
    STOPPING --> EXITED: exit or kill escalation
    CREATING --> FAILED: setup failure
    CREATED --> FAILED: launch failure
    RUNNING --> FAILED: unrecoverable inconsistency
    STOPPING --> FAILED: unrecoverable inconsistency
    EXITED --> DELETING: cleanup requested
    FAILED --> DELETING: cleanup requested
    DELETING --> [*]: cleanup complete / record deleted last
    classDef pending fill:#e8f0ff,stroke:#3563a4,color:#183b6a
    classDef active fill:#e5f5f0,stroke:#23836b,color:#155b49
    classDef terminal fill:#f1edff,stroke:#7657a8,color:#4c3575
    classDef failure fill:#fff4db,stroke:#b9892a,color:#634615
    class CREATING,CREATED pending
    class RUNNING active
    class STOPPING,EXITED,DELETING terminal
    class FAILED failure
```

**Key:** blue = preparation; green = executing; purple = stopping/cleanup;
amber = failure. Start/end markers mean no local record. Arrows are legal
state transitions; deleting a record is not a stored `ABSENT` phase.

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
