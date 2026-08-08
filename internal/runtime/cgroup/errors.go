//go:build linux

// Package cgroup owns all Linux cgroup v2 mechanics for Glider (Phase 4,
// docs/design/cgroups.md): discovering the host's cgroup v2 topology,
// bootstrapping a delegated Glider-owned subtree, creating/configuring/
// attaching/removing one cgroup per container, and reading back typed
// resource statistics. The process launcher (internal/runtime/process)
// consumes this package rather than manipulating cgroup files itself.
package cgroup

import "errors"

// ErrUnsupported means the host does not provide a usable cgroup v2
// unified hierarchy at all (ADR-0001 requires cgroup v2 unconditionally;
// this package never falls back to v1).
var ErrUnsupported = errors.New("cgroup v2 unified hierarchy not available")

// ErrNotDelegated means Glider could not obtain a cgroup subtree it can
// configure controllers for — most commonly because some other,
// non-Glider process shares the cgroup Glider would need to enable
// controllers in, and Glider will not move a process it doesn't own to
// work around that (docs/design/cgroups.md "Delegation"). This is a
// distinct, actionable condition from ErrUnsupported: cgroup v2 exists,
// but this host/environment hasn't delegated a usable subtree to Glider.
var ErrNotDelegated = errors.New("cgroup v2 controllers not delegated to glider")

// ErrInvalidResource means a resource specification (CPU/memory/PIDs)
// failed validation before any cgroup or file was touched.
var ErrInvalidResource = errors.New("invalid resource specification")

// ErrInvalidContainerID means a container ID failed the strict validation
// cgroup path construction requires (docs/design/cgroups.md "Naming") —
// never accepted as a literal path component without it, since it can
// reach this package from an operator-supplied CLI argument
// (`glider-runtime recover <id>`), not just internally-generated IDs.
var ErrInvalidContainerID = errors.New("invalid container id")

// ErrPopulated means a cgroup still has member processes and cannot be
// removed yet.
var ErrPopulated = errors.New("cgroup still has member processes")
