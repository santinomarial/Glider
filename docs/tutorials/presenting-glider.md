# Presenting Glider

Use this guide to record a short portfolio demo or prepare an engineering
walkthrough. Keep every visual tied to code or observed results, and identify
which demonstration uses fixture health reports and which uses live containers.

## A 90-second recording

| Time | Screen | Explanation |
|---|---|---|
| 0–15 seconds | README title and architecture | Glider implements container execution and distributed reconciliation in Go on Linux |
| 15–30 seconds | Architecture diagram | Desired state lives in etcd; the scheduler assigns work and node agents reconcile it |
| 30–60 seconds | Terminal running `make demo` | Explain the two replicas, service lookup, and image update; disclose simulated health and endpoints |
| 60–80 seconds | One design decision and its tests | Explain why a generation token rejects stale node actions, or why readiness gates replacement |
| 80–90 seconds | Repository links and status | Point to the code, docs, and remaining environment qualification |

Run the [tutorial](local-demo.md) before recording. Increase the terminal font
size, remove unrelated windows, and use captions. Keep commands and output
available as text alongside the video. Do not show credentials or operator
metadata from an actual cluster.

## A live Linux demonstration

A deeper demonstration needs an isolated Linux cluster configured through the
[installation](../operations/install.md), [PKI](../operations/pki.md), and
[monitoring](../operations/monitoring.md) guides. Rehearse these outcomes:

1. Submit a workload and show real task assignments and running processes.
2. Reach its service and observe application responses.
3. Update the image while observing readiness and request results.
4. Introduce a controlled failure in the disposable environment and observe
   reconciliation. Explain the scope of the failure and what the result proves.
5. Show relevant metrics and events, with timestamps that match the experiment.

Capture a backup recording. Publish the commit, host topology, commands,
workload, and observations with it. A scripted control-plane demo cannot
substitute for these experiments.

## Portfolio case study

Use a short project summary followed by the demo, architecture, and two or
three specific engineering decisions. Explain your contribution accurately,
including external dependencies and development assistance where relevant.
Describe what you learned and what remains incomplete.

Suggested summary:

> Glider is a distributed container platform written in Go. It implements a
> Linux runtime, OCI image pipeline, network stack, and etcd-backed control
> plane. The project explores how durable assignments, reconciliation, node
> leases, and generation fencing support container orchestration under failure.

Link performance claims to a report that identifies the source commit,
hardware, workload, units, and measurement method. Distinguish simulated
control-plane benchmarks from kernel runtime measurements. Avoid unsupported
comparisons with production orchestrators.

## Presentation outline

1. **Glider**: scope and one-sentence description.
2. **Motivation**: the container and distributed-systems questions explored.
3. **Architecture**: ownership, durable state, and node reconciliation.
4. **Container lifecycle**: image, snapshot, network, isolation, process.
5. **Scheduling and reconciliation**: assignment identity and convergence.
6. **Failure behavior**: leases, stale generations, and partition limitations.
7. **Verification**: security tests, recovery exercises, and evidence boundaries.
8. **Demonstration and lessons**: observed outcomes, trade-offs, remaining work.

Use one main idea per slide. Prefer diagrams, measured output, and a concise
speaker explanation to copying lists from the README. Keep status consistent
with the [production-readiness contract](../release/production-readiness.md).
