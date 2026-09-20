# Production deployment view

This view describes a reference topology and its failure domains.
It is a deployment model, not proof that a particular installation has passed
qualification; evidence requirements remain in
[production readiness](../release/production-readiness.md).

## Request routing and authority

Replicated groups are collapsed here so the routing stays readable. The
placement table below expands those groups across independent failure domains.

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff","clusterBkg":"#f8fafc","clusterBorder":"#cbd5e1"},"flowchart":{"curve":"basis","nodeSpacing":40,"rankSpacing":50}}}%%
flowchart TB
    accTitle: Reference deployment — API routing and direct etcd sessions
    accDescr: An L4 balancer passes mTLS to API replicas. Workers use that endpoint for secrets but connect directly to the three-member etcd quorum for watches, leases and status.
    clients(["Operators + automation"]):::person
    lb["L4 load balancer<br/>TCP pass-through · stable API endpoint"]:::external
    apis["Control-plane hosts<br/>3 API replicas · glider-controlplane<br/>One leads controller loops"]:::control
    store[("3 etcd members · A / B / C<br/>2-of-3 quorum · peer mTLS<br/>Durable state + leader election")]:::store
    nodes["Worker hosts<br/>2+ Linux workers · gliderd<br/>Direct etcd client sessions"]:::worker
    clients -->|"mTLS gRPC"| lb
    lb -->|"Forward TCP · preserve client TLS"| apis
    apis -->|"Transactions + election · etcd mTLS"| store
    nodes -->|"Watch / lease / status · etcd mTLS"| store
    nodes -->|"Secret delivery requests · mTLS gRPC"| lb
    classDef person fill:#172b4d,stroke:#172b4d,color:#ffffff
    classDef control fill:#e8f0ff,stroke:#3563a4,color:#183b6a
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
    classDef worker fill:#e5f5f0,stroke:#23836b,color:#155b49
    classDef external fill:#f1f4f8,stroke:#78879c,color:#334155
```

**Key:** dark = clients; gray = routing infrastructure; blue = API/controller
processes; purple cylinder = replicated durable state; green = workers. Arrows
show connection initiators. Replies and watch events return on the same
connections. The API load balancer does not proxy etcd watches or leases.

## Failure-domain placement

| Role | Domain A | Domain B | Domain C |
|---|---|---|---|
| API replica | Control plane 1 | Control plane 2 | Control plane 3 |
| Durable state | etcd member 1 | etcd member 2 | etcd member 3 |
| Workload execution | Worker 1 | Worker 2 | Optional additional workers |

Domains must correspond to independent host or infrastructure failures for
the availability claim being made. Roles in a column need not share a host.
Placing every member in one VM or one storage failure domain tests process
failover, not host availability. etcd's Raft leader and Glider's controller
leader are separate elections.

## External operations

This view isolates monitoring, image distribution, and backup dependencies
from the critical scheduling path. It shows service groups, not host placement.

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff"},"flowchart":{"curve":"basis","nodeSpacing":35,"rankSpacing":50}}}%%
flowchart LR
    accTitle: External operations — metrics, images and encrypted backups
    accDescr: Prometheus scrapes every API replica. Workers pull OCI images. A backup job takes an etcd snapshot and an operator-managed copy moves the encrypted artifact off-host.
    monitor["Prometheus + paging<br/>External monitoring"]:::external
    apis["Every API replica<br/>HTTPS metrics endpoint"]:::control
    nodes["Linux workers<br/>OCI image pipeline"]:::worker
    registry["OCI registry<br/>Manifests + blobs"]:::external
    job["Backup job<br/>glider-admin + systemd timer"]:::control
    store[("etcd quorum")]:::store
    vault[("Immutable off-host storage")]:::store
    monitor -->|"Scrape · HTTPS / mTLS"| apis
    nodes -->|"Pull · HTTPS OCI API"| registry
    job -->|"Snapshot request · etcd mTLS"| store
    job -->|"Encrypted artifact<br/>Operator-managed copy"| vault
    classDef control fill:#e8f0ff,stroke:#3563a4,color:#183b6a
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
    classDef worker fill:#e5f5f0,stroke:#23836b,color:#155b49
    classDef external fill:#f1f4f8,stroke:#78879c,color:#334155
```

**Key:** blue = operational Glider process; green = workers; gray = external
service; purple cylinder = durable storage. The backup job encrypts snapshots
locally; an independently configured job copies them off-host. Certificate
issuance and renewal apply to all authenticated endpoints and are omitted
from these paths; see [PKI](../operations/pki.md).

## Quorum and failure behavior

| Failure | Expected behavior | Qualification evidence |
|---|---|---|
| One control-plane process or host | Load balancer removes it; API remains available | Requests through the stable endpoint during termination |
| One etcd member | Two-member majority continues committing | Linearizable mutation while the member is unavailable |
| One worker | Lease expires; assignments are fenced and replaced | No stale status accepted; workloads recover within the declared SLO |
| Control-plane network partition | Only the quorum side commits; isolated authorities stop | No split scheduling authority |
| Registry outage | Cached images may start; uncached pulls fail explicitly | No unverified or partial image becomes runnable |
| Backup destination outage | Alert fires; local operation continues without claiming a valid RPO | Pager delivery and later successful immutable copy |

## Capacity floor

The baseline is three control-plane replicas, three etcd members, and at least
two workers. Production sizing must reserve host capacity for the OS, image
unpack, logs, and recovery bursts. See [capacity and sizing](../operations/sizing.md).

## Security zones

- The public or operator-facing API boundary contains only the load balancer
  and mTLS API; etcd is reachable only over restricted internal connections.
- etcd peer ports admit members only. Client access is restricted to the
  configured control-plane, worker, and administrative identities; workers
  need direct etcd connectivity in the current implementation.
- Worker nodes receive only assignment-scoped secrets for their current
  generation.
- Backup storage is a distinct failure and credential domain.
- Monitoring is read-only except for its external notification path.

Related: [high availability](../operations/high-availability.md),
[PKI](../operations/pki.md), [backup and restore](../operations/backup-restore.md),
and [environment evidence](../release/environment-evidence.md).
