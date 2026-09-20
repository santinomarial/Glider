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

## Visual language

Each view answers one question. The container view separates process-level
connections from worker internals; the deployment view separates routing from
failure-domain placement and external operations. Runtime sequences highlight
commit points and concurrent failure handling.

| Visual | Meaning |
|---|---|
| Dark rounded node | Person or operator-facing tool |
| Blue box | Glider control-plane or administrative logic |
| Green box | Worker execution and local resources |
| Purple cylinder | Durable state or stored artifacts |
| Gray box | External service or routing infrastructure |
| Amber state or note | Failure path or timing caveat |
| Labeled enclosure | Process, host, or phase boundary as explicitly named |

Color is supplementary: names, shapes, and each figure's key carry the same
distinctions. State and sequence diagrams include their own local legends.

## Reading connections

- A solid arrow is the labeled call, write, transition, or artifact handoff.
- A dashed arrow delivers watch events or returns data/replies.
- Solid arrows in static connection views point from client to server, including watch
  subscriptions. Dynamic views show the returning events explicitly.
- Edge labels name the protocol on every cross-process connection.
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

Every figure includes an accessible title and description. Mermaid source
lives beside the explanation and renders directly on GitHub. Validate all
figures and local links with `make docs`. To export SVG and PNG copies for
slides or a portfolio, run:

```bash
make diagrams
```

Exports go to `dist/diagrams/`, with names derived from the source document
and its diagram number. Markdown is the source of truth; exports are generated
artifacts. Use the white-background PNG for slide software that does not fully
support SVG text rendering. Inspect labels, edge crossings, and readability at
the intended display size; a successful parser is not a visual review.

## Design references

The redesign draws on projects found in the
[architecture-diagram topic](https://github.com/topics/architecture-diagram):
[C4-PlantumlSkin](https://github.com/skleanthous/C4-PlantumlSkin) demonstrates
separate context/container levels and explicit element types;
[svg-diagram](https://github.com/bybit-exchange/svg-diagram) emphasizes semantic
color, spacing, and reviewing the rendered result. Glider retains Mermaid for
GitHub-native editing and rendering rather than adding either tool as a runtime
dependency. The [C4 notation guide](https://c4model.com/diagrams/notation)
informs titles, keys, responsibilities, and labeled relationships.

The diagrams are specific to Glider. Connections are checked against
[node startup](../../cmd/gliderd/main.go),
[administrative commands](../../cmd/glider-admin/main.go),
[node resource setup](../../internal/agent/runtime_driver_linux.go), and
[runtime state transitions](../../internal/runtime/process/state/state.go).
