# ADR 0001: Linux, cgroup v2 only

## Status

Accepted.

## Context

Glider needs a resource-control mechanism for CPU, memory, and PID-count
limits (master plan §12). Linux has two cgroup APIs: v1 (per-controller
hierarchies, e.g. separate `cpu`, `memory`, `pids` trees that can be mounted
independently and even attached differently per controller) and v2 (a
single unified hierarchy, all controllers under one tree, standardized
delegation model). Most current distributions default to the v2 unified
hierarchy under systemd; v1 is still present on older systems and some
non-systemd setups.

## Problem

Supporting both APIs means either (a) two parallel implementations of
resource control, doubling the surface area to get right and test, or (b)
an abstraction layer that hides which one is in use, which tends to leak —
v1 and v2 differ in more than syntax (e.g. v1's `memory` and `pids`
controllers can attach at different hierarchy points than `cpu`; v2
enforces a single consistent tree, which changes how nested/delegated
cgroups work, relevant later for per-container sub-hierarchies).

## Decision

Target **cgroup v2 only**, unified hierarchy, unconditionally. Glider
refuses to start (or run a container requiring resource limits) on a host
without a v2 unified `/sys/fs/cgroup`, rather than silently degrading to no
enforcement or attempting v1 compatibility.

## Alternatives considered

- **Support both v1 and v2** (the `libcontainer`/runc approach, which
  supports both because it must run on the entire installed base of
  production Docker/Kubernetes hosts). Rejected: Glider is not trying to
  run on arbitrary legacy production fleets; it is a from-scratch project
  where the whole point is understanding the resource-control mechanism
  deeply, and maintaining two implementations dilutes that for marginal
  compatibility benefit.
- **v1 only.** Rejected: v1 is the legacy API: no new distributions default
  to it, and its design (independent per-controller hierarchies) is
  explicitly what v2 was created to fix. Building new depth on a
  deprecated model is the wrong investment.
- **Runtime auto-detection with a v1 fallback path stubbed out.** Rejected
  as a false economy — a "fallback" that isn't actually implemented is
  worse than an explicit refusal to start, because it invites silently
  unenforced resource limits (a security-relevant bug, not just an
  inconvenience) rather than a clear startup error.

## Consequences

- Test/dev/CI hosts must run a kernel and distro with cgroup v2 as the
  unified hierarchy (modern kernel; most current major distributions
  qualify by default).
- The cgroup subsystem's code (Phase 4) is meaningfully simpler: one
  hierarchy shape, one delegation model, no controller-interface
  compatibility shims.
- Older or unusual host configurations (cgroup v1, or "hybrid" mode) are
  unsupported; Glider fails fast with a clear error rather than attempting
  degraded operation.

## Risks

- Narrows the set of hosts Glider runs on. Acceptable: master plan §46/§36
  already scope this project around depth over portability, and cgroup v2
  is standard on any reasonably current Linux install.

## What would cause reconsideration

Evidence that a cgroup v2-only requirement blocks a specific, valuable
testing or demo environment that cannot reasonably be upgraded (e.g. a
required CI runner stuck on an old kernel). Would need a concrete blocked
scenario, not a hypothetical one, to reopen this decision.
