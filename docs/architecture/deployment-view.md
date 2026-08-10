# Production deployment view

This view describes the minimum qualified topology and its failure domains.
It is a deployment model, not proof that a particular installation has passed
qualification; evidence requirements remain in
[production readiness](../release/production-readiness.md).

## Topology

```mermaid
flowchart TB
    clients["Operators and automation"]
    lb["Layer 4 load balancer<br/>Stable control-plane endpoint"]
    registry["Production OCI registry"]
    backup["Immutable off-host backup storage"]
    monitoring["External monitoring and paging"]
    pki["Certificate manager"]

    subgraph zoneA["Failure domain A"]
        cp1["Control plane 1"]
        e1["etcd member 1"]
        w1["Worker 1<br/>gliderd + workloads"]
    end

    subgraph zoneB["Failure domain B"]
        cp2["Control plane 2"]
        e2["etcd member 2"]
        w2["Worker 2<br/>gliderd + workloads"]
    end

    subgraph zoneC["Failure domain C"]
        cp3["Control plane 3"]
        e3["etcd member 3"]
    end

    clients -->|"mTLS gRPC"| lb
    lb --> cp1
    lb --> cp2
    lb --> cp3
    cp1 -->|"mTLS etcd client"| e1
    cp2 -->|"mTLS etcd client"| e2
    cp3 -->|"mTLS etcd client"| e3
    e1 <-->|"Raft peer mTLS"| e2
    e2 <-->|"Raft peer mTLS"| e3
    e3 <-->|"Raft peer mTLS"| e1
    w1 -.->|"mTLS watch, status, lease"| lb
    w2 -.->|"mTLS watch, status, lease"| lb
    w1 -->|"HTTPS image pull"| registry
    w2 -->|"HTTPS image pull"| registry
    e1 -->|"encrypted scheduled snapshot"| backup
    monitoring -.->|"scrape"| cp1
    monitoring -.->|"scrape"| cp2
    monitoring -.->|"scrape"| cp3
    monitoring -.->|"scrape"| w1
    monitoring -.->|"scrape"| w2
    pki -.->|"issue and renew"| cp1
    pki -.->|"issue and renew"| cp2
    pki -.->|"issue and renew"| cp3
    pki -.->|"issue and renew"| w1
    pki -.->|"issue and renew"| w2
```

The three failure-domain subgraphs must map to genuinely independent host or
infrastructure failures for the availability claim being made. Placing all
members in one VM, one host, or one non-redundant storage domain exercises
process failover but does not qualify host-level high availability.

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

- The public or operator-facing boundary terminates only at the load balancer
  and mTLS API; etcd is never an operator endpoint.
- etcd peer and client ports are restricted to control-plane identities.
- Worker nodes receive only assignment-scoped secrets for their current
  generation.
- Backup storage is a distinct failure and credential domain.
- Monitoring is read-only except for its external notification path.

Related: [high availability](../operations/high-availability.md),
[PKI](../operations/pki.md), [backup and restore](../operations/backup-restore.md),
and [environment evidence](../release/environment-evidence.md).
