//go:build linux

// Command glider-runtime is the Phase 1/2 standalone container launcher
// described in docs/design/runtime.md §6:
//
//	sudo glider-runtime run --rootfs <path> -- <cmd> [args...]
//
// Phase 2 (docs/adr/0006) adds a `recover` subcommand — the smallest
// possible crash-recovery surface, since no daemon (`gliderd`) exists yet
// to run it automatically. It is intentionally documented as an
// internal/debug tool, not a stable user-facing API: a future `gliderd`
// is expected to call the equivalent internal API (process.Recover)
// directly rather than shelling out to this subcommand.
//
// It has no daemon, no image handling, and no cgroup/network/security
// enforcement — see runtime.md §6 for the exact, frozen scope boundary.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"github.com/santinomarial/glider/internal/runtime/process"
)

// exitSupervisorFailure is returned by the CLI when the container
// supervisor (glider-init) itself failed after the workload was already
// RUNNING — distinct from a workload's own exit code (any value 0-255,
// returned unchanged) and distinct from a pre-RUNNING setup failure (exit
// 1, unchanged from Phase 1). Matches the Docker CLI's convention of
// reserving a value outside the normal workload range for "the runtime
// itself errored" (runtime.md §7).
const exitSupervisorFailure = 125

func main() {
	// Must be checked before any flag parsing: this is the re-exec
	// entrypoint (runtime.md §1), not a user-facing subcommand, and it
	// assumes a very specific fd/env contract that only process.Run
	// (launcher.go) sets up. See process.RunInit's doc comment.
	if len(os.Args) > 1 && os.Args[1] == process.ReexecArg {
		process.RunInit(os.Args[2:])
		// RunInit always terminates the process — reaching here is itself
		// a bug.
		fmt.Fprintln(os.Stderr, "glider-runtime: internal error: __glider_init__ returned")
		os.Exit(2)
	}
	if len(os.Args) > 1 && os.Args[1] == process.ReexecWorkloadArg {
		process.RunWorkload(os.Args[2:])
		fmt.Fprintln(os.Stderr, "glider-runtime: internal error: __glider_workload__ returned")
		os.Exit(2)
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "recover":
		os.Exit(recoverCmd(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: glider-runtime run --rootfs <path> [--hostname <name>] [--cpus <n>] [--memory <size>] [--pids <n>] -- <cmd> [args...]")
	fmt.Fprintln(os.Stderr, "       glider-runtime recover --state-dir <dir> <container-id>")
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	rootfs := fs.String("rootfs", "", "path to a pre-existing root filesystem directory (required)")
	hostname := fs.String("hostname", "glider", "hostname to set inside the container's UTS namespace")
	stateDir := fs.String("state-dir", "", "override the default state directory (/var/lib/glider/containers); primarily for tests")
	stopGrace := fs.Duration("stop-grace", 0, "grace period between forwarding SIGTERM and escalating to SIGKILL (default 10s if unset or zero)")
	cpus := fs.String("cpus", "", "fractional CPU bandwidth limit, e.g. 0.5 or 2.5 (unset = unlimited)")
	memory := fs.String("memory", "", "hard memory limit, e.g. 256Mi or 1Gi or a raw byte count (unset = unlimited)")
	pids := fs.String("pids", "", "maximum tasks (processes+threads) in the container, including glider-init itself (unset = unlimited)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	argv := fs.Args()
	if *rootfs == "" {
		usage()
		fmt.Fprintln(os.Stderr, "glider-runtime: --rootfs is required")
		return 2
	}
	if len(argv) == 0 {
		usage()
		fmt.Fprintln(os.Stderr, "glider-runtime: no workload command specified after --")
		return 2
	}

	var resources cgroup.Resources
	if *cpus != "" {
		v, err := cgroup.ParseCPUCores(*cpus)
		if err != nil {
			fmt.Fprintln(os.Stderr, "glider-runtime:", err)
			return 2
		}
		resources.CPUCores = v
	}
	if *memory != "" {
		v, err := cgroup.ParseMemoryBytes(*memory)
		if err != nil {
			fmt.Fprintln(os.Stderr, "glider-runtime:", err)
			return 2
		}
		resources.MemoryBytes = v
	}
	if *pids != "" {
		v, err := cgroup.ParsePIDsMax(*pids)
		if err != nil {
			fmt.Fprintln(os.Stderr, "glider-runtime:", err)
			return 2
		}
		resources.PIDsMax = v
	}

	id, err := process.NewContainerID()
	if err != nil {
		fmt.Fprintln(os.Stderr, "glider-runtime:", err)
		return 1
	}

	cfg := process.Config{
		RootFS:      *rootfs,
		Argv:        argv,
		Hostname:    *hostname,
		StateDir:    *stateDir,
		ContainerID: id,
		StopGrace:   *stopGrace,
		Resources:   resources,
	}

	// SIGTERM/SIGINT delivered to glider-runtime itself (e.g. an operator
	// or test sending it to this process, matching runtime.md §6 exit-gate
	// (d)) become a stop request forwarded into the container by
	// process.Run, preserving which of the two it actually was — see its
	// doc comment. signal.Notify (not NotifyContext) is used deliberately:
	// a bare context.Done() discards which signal triggered it, and Run
	// needs the actual value to forward it faithfully rather than
	// normalizing every stop request to SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)

	exitCode, err := process.Run(stop, cfg)
	if err != nil {
		var supErr *process.SupervisorFailureError
		if errors.As(err, &supErr) {
			fmt.Fprintln(os.Stderr, "glider-runtime: container supervisor error:", supErr)
			return exitSupervisorFailure
		}
		fmt.Fprintln(os.Stderr, "glider-runtime:", err)
		return 1
	}
	return exitCode
}

func recoverCmd(args []string) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "state directory root (required; matches the --state-dir the container was run with)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	argv := fs.Args()
	if *stateDir == "" || len(argv) != 1 {
		usage()
		fmt.Fprintln(os.Stderr, "glider-runtime: recover requires --state-dir and exactly one container-id")
		return 2
	}
	containerID := argv[0]

	result, err := process.Recover(*stateDir, containerID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "glider-runtime: recover:", err)
		return 1
	}

	fmt.Printf("container %s: %s (phase=%s)\n", result.ContainerID, result.Action, result.Record.Phase)
	return 0
}
