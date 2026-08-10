# Architecture map and diagram conventions

This directory explains Glider at progressively deeper levels. The views use
C4 terminology for static structure, plus deployment and dynamic views for
runtime behavior.

## View map

| View | Question answered | Primary audience |
|---|---|---|
| [System context](system-context.md) | Who uses Glider, and what external systems does it depend on? | Operators, security reviewers, new contributors |
| [Container view](container-view.md) | Which deployable processes own which responsibilities and data? | Maintainers, reviewers, SREs |
| [Deployment view](deployment-view.md) | Where do processes run, and which failures are independent? | Platform engineers, SREs |
| [Runtime flows](runtime-flows.md) | How does desired state become a running container, and how is stale authority fenced? | Maintainers, incident responders |
| [Architecture overview](overview.md) | Which principles, identifiers, and state machines govern the design? | All technical readers |

## Notation

- A solid arrow is a synchronous request or durable write.
- A dashed arrow is a watch, stream, heartbeat, or asynchronous observation.
- Edge labels name the protocol or mechanism.
- A subgraph is a process, host, trust zone, or failure domain as labeled.
- `etcd` is the authoritative cluster-state store. Node-local state is not a
  substitute for current assignment authority.
- “Container” in these documents means a deployable/executable unit in the C4
  sense only when discussing the container view; workload containers are
  explicitly called workload containers.

## Diagram maintenance

Diagrams describe the current system, not a future roadmap. When a process,
protocol, trust boundary, or state owner changes, update the diagram in the
same commit as the implementation and link the governing ADR or design page.
See the [documentation standard](../contributing/documentation.md) for review
criteria.
