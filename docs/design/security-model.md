# Security model

Status: Phase 8 implemented against this contract.
Related: [runtime.md](runtime.md) (namespace/mount mechanics this builds on),
[container-lifecycle.md](container-lifecycle.md).

## 1. Threat model

**In scope — Glider aims to contain:**

- A workload container attempting to read/write the host filesystem outside
  its assigned root.
- A workload container attempting to see or signal processes outside its
  PID namespace.
- A workload container attempting to consume unbounded CPU, memory, or
  process-count resources and starving the node or other containers.
- A workload container attempting to escalate privilege via `setuid`
  binaries, capability inheritance, or `execve`-time privilege gain
  (`no_new_privs`, §4).
- A workload container attempting known container-escape syscall patterns
  (namespace manipulation, privileged mount operations, raw kernel module
  loading, `ptrace` of host processes) — addressed by the default seccomp
  profile (§5).

**Explicitly out of scope — not defended against:**

- **Kernel vulnerabilities.** Glider containers share the host kernel.
  Glider is not a VM boundary and does not claim to be. A kernel privilege-
  escalation bug is a host compromise regardless of Glider's configuration.
  This is stated plainly, not hedged, because pretending otherwise would
  violate the "no fake guarantees" principle (overview.md §2).
- **Byzantine/malicious cluster nodes or control-plane components.** The
  failure model (failure-model.md) assumes fail-stop/fail-partition
  behavior, not adversarial insiders.
- **Side-channel attacks** (cache timing, speculative execution) between
  co-located containers — a multi-tenant hardware isolation problem outside
  a shared-kernel container runtime's reach.
- **Supply-chain compromise of images** beyond digest verification (Glider
  verifies the bytes it pulls match the digest it resolved; it does not
  vet image *contents* for malicious behavior).

## 2. Defense layers

Layers are cumulative, not alternatives — each narrows what a workload can
do regardless of whether another layer has a gap:

```text
namespaces (runtime.md §2)         — what the process can see
    +
filesystem isolation (runtime.md §4) — what the process can reach
    +
cgroup v2 (Phase 4)                — what the process can consume
    +
capability reduction (§3)          — what privileged operations are available at all
    +
no_new_privs (§4)                  — whether privilege can increase later
    +
seccomp (§5)                       — which syscalls are reachable regardless of capability
```

Seccomp alone is explicitly rejected as a sufficient sandbox (master plan
§16) — a syscall filter without namespace isolation still lets a process
see and potentially signal host processes; without filesystem isolation
still lets it read host files; without capability reduction still lets
allowed syscalls do privileged things. Each layer closes a gap the others
don't.

## 3. Capabilities

Default posture: **deny by default, add back only what's justified.**
Glider's default container does not inherit full host root capabilities
even when the container process's UID is 0 inside its namespace.

The workload trampoline reduces the bounding set before the workload's
`execve`, from outside any capability the workload could try to re-acquire.
The concrete allowlist is documented in §7. Capabilities excluded include
`CAP_SYS_ADMIN` (a well-known "root-equivalent grab
bag" — mount, namespace, and other operations), `CAP_SYS_MODULE`,
`CAP_SYS_BOOT`, `CAP_SYS_PTRACE` (host-process tracing), `CAP_SYS_RAWIO`,
`CAP_NET_ADMIN`.

Bounding, permitted, effective, inheritable, and ambient sets are each
configured deliberately rather than left at their post-`clone()` defaults —
in particular, ambient capabilities are empty by default so a capability
present in the bounding set is not automatically usable by a non-root
in-container process.

## 4. no_new_privs

`PR_SET_NO_NEW_PRIVS` is set in the workload trampoline before `execve` of the
workload (runtime.md §4 sequence, immediately prior to the final exec).
This guarantees `execve` cannot grant new privileges via `setuid`/`setgid`
binaries or file capabilities inside the container, closing the specific
gap where a reduced-capability process could otherwise re-escalate by
executing a privileged binary shipped in the image.

## 5. seccomp

The default seccomp profile is a **deny-list built from an explicit threat
model**, not a list of syscalls that happened to seem scary (master plan
§16 — "do not randomly block syscalls"). Categories justified for blocking,
each tied to the escape/attack pattern it closes:

| Category | Example syscalls | Why blocked |
|---|---|---|
| Kernel module management | `init_module`, `delete_module`, `finit_module` | loading arbitrary kernel code from inside a container is a direct host compromise |
| Reboot / system control | `reboot`, `kexec_load` | denial of service / host takeover from inside a container |
| Privileged mount operations | `mount`, `umount2` (outside the narrow set the runtime itself performs before dropping privilege), `pivot_root` (workload never needs this — only Glider's own init uses it) | mount manipulation is a classical container-escape vector |
| Process tracing | `ptrace`, `process_vm_readv`, `process_vm_writev` | prevents inspecting/controlling processes outside (or even within) the container's intended boundary |
| Namespace manipulation | `unshare`, `setns` (workload does not need to create or join namespaces itself) | prevents a workload from constructing its own namespace escape path |
| Raw privileged interfaces | `bpf`, `perf_event_open`, `kexec_file_load`, `open_by_handle_at` | broad kernel-internal access with a history of privilege-escalation bugs |

The implemented filter follows these categories; §7 states its exact initial
coverage and deliberate compatibility choices.

## 6. Validation

Master plan §16/§34's adversarial test programs (`test/security/`:
ptrace attempt, mount attempt, fork bomb, raw socket attempt, namespace
creation, reboot attempt, host process visibility, host filesystem
visibility) are the acceptance criteria for this document, implemented
starting Phase 8. A security claim in this doc that isn't backed by one of
these tests by the time Phase 8 closes is a gap to fix, not a doc to
quietly leave aspirational.

## 7. Phase 8 implementation

Security is applied to the workload child, not `glider-init`. Immediately
before the real workload `execve`, an internal trampoline:

1. drops every capability from the bounding set except the default allowlist;
2. sets permitted/effective to that allowlist and clears inheritable/ambient;
3. sets `PR_SET_NO_NEW_PRIVS`;
4. installs the amd64 seccomp-BPF policy; and
5. executes the workload.

A CLOEXEC status pipe preserves the lifecycle guarantee: `RUNNING` is
reported only after the trampoline becomes the real workload. Policy or exec
failure remains a pre-RUNNING `FAILED` transition.

The retained capabilities are `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`,
`CAP_FSETID`, `CAP_KILL`, `CAP_SETGID`, `CAP_SETUID`, and
`CAP_NET_BIND_SERVICE`. Administrative, ptrace, module, raw-network, and raw
I/O capabilities are absent.

The filter returns `EPERM` for mount/unmount/pivot-root, ptrace, reboot,
module operations, kexec, open-by-handle, namespace creation/join, BPF, and
perf-event access. An architecture mismatch kills the process; non-amd64
installation is rejected because Glider remains amd64-first. Broad process
primitives such as `clone`, required by ordinary language runtimes for
threads, are intentionally allowed.

The privileged test executes the installed policy in a subprocess, verifies
`NoNewPrivs: 1` and seccomp filter mode through `/proc/self/status`, and
requires a forbidden `unshare` call to fail with `EPERM`.
