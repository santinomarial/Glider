# System context

This view answers who interacts with Glider and which external systems are
inside the production dependency boundary. It deliberately hides internal
processes; see the [container view](container-view.md) to zoom in.

## Context diagram

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontFamily":"Arial, sans-serif","fontSize":"16px","lineColor":"#64748b","primaryTextColor":"#172b4d","edgeLabelBackground":"#ffffff"},"flowchart":{"curve":"basis","nodeSpacing":35,"rankSpacing":50}}}%%
flowchart TB
    accTitle: Glider system context — people and external dependencies
    accDescr: Operators and workload owners use Glider through the CLI. External systems supply images and certificates, scrape metrics, and retain encrypted backups.
    people(["Platform operators + workload owners<br/>People · lifecycle and workload intent"]):::person
    glider["GLIDER<br/>Software system · Go / Linux<br/>Schedule, isolate and reconcile OCI workloads"]:::control
    registry["OCI registry<br/>External system · manifests and blobs"]:::external
    identity["Certificate manager<br/>External system · identity issuance"]:::external
    monitor["Prometheus + alert delivery<br/>External system · visibility and paging"]:::external
    backup[("Off-host backup storage<br/>External system · immutable retention")]:::store
    people -->|"Workloads + operations<br/>mTLS gRPC via CLI"| glider
    glider -->|"Resolve + pull verified images<br/>HTTPS OCI Distribution"| registry
    identity -->|"Provision and renew<br/>X.509 certificates"| glider
    monitor -->|"Scrape API metrics<br/>HTTPS with mTLS"| glider
    glider -->|"Operator-managed copy<br/>Encrypted snapshots"| backup
    classDef person fill:#172b4d,stroke:#172b4d,color:#ffffff
    classDef control fill:#e8f0ff,stroke:#3563a4,color:#183b6a,stroke-width:2px
    classDef external fill:#f1f4f8,stroke:#78879c,color:#334155
    classDef store fill:#f1edff,stroke:#7657a8,color:#4c3575
```

**Key:** dark rounded node = people; blue = Glider; gray = external service;
purple cylinder = durable storage. Arrows follow the labeled request or
delivery. Certificate provisioning and off-host copying are operator-managed
integrations, not built-in remote services. Monitoring initiates scrapes.

Release reviewers inspect signed artifacts and environment evidence outside
the runtime request path. Image content is accepted only after digest
verification; backup objects are encrypted and authenticated before copying.

## Responsibilities

| Actor or system | Owns | Does not own |
|---|---|---|
| Platform operator | Cluster lifecycle, capacity, credentials, upgrades, incident response | Reconciliation decisions or task placement transactions |
| Workload owner | Desired workload, service, secret references, rollout intent | Direct node commands or assignment authority |
| Glider | Admission, durable desired state, scheduling, reconciliation, runtime isolation, status | Registry availability, external certificate policy, off-host storage durability |
| OCI registry | Availability of declared manifests and blobs | Trust in mutable tags; Glider resolves and verifies digests |
| PKI / certificate manager | Identity issuance, expiry, revocation policy | Glider RBAC and generation fencing |
| Backup storage | Off-host immutable retention | Snapshot correctness or restore authorization |
| Monitoring and paging | Signal retention and operator delivery | Remediation authority unless an operator explicitly automates it |

## Trust boundaries

Glider treats operator clients, nodes, and external services as separately
authenticated principals. A valid network path is not authorization. The API
enforces certificate identity and role policy; node assignment and secret
delivery additionally require the current node and task generation.

Related: [control-plane security](../design/control-plane-security.md),
[security model](../design/security-model.md), and
[production hardening](../operations/hardening.md).
