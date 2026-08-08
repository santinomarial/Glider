//go:build linux

// Command glider-runtime is the Phase 1 standalone container launcher
// described in docs/design/runtime.md §6:
//
//	sudo glider-runtime run --rootfs <path> -- <cmd> [args...]
//
// It has no daemon, no image handling, and no cgroup/network/security
// enforcement — see runtime.md §6 for the exact, frozen scope boundary.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/santinomarial/glider/internal/runtime/process"
)

func main() {
	// Must be checked before any flag parsing: this is the re-exec
	// entrypoint (runtime.md §1), not a user-facing subcommand, and it
	// assumes a very specific fd/env contract that only process.Run
	// (launcher.go) sets up. See process.RunInit's doc comment.
	if len(os.Args) > 1 && os.Args[1] == process.ReexecArg {
		process.RunInit(os.Args[2:])
		// RunInit always terminates the process (exec into the workload,
		// or os.Exit on failure) — reaching here is itself a bug.
		fmt.Fprintln(os.Stderr, "glider-runtime: internal error: __glider_init__ returned")
		os.Exit(2)
	}

	if len(os.Args) < 2 || os.Args[1] != "run" {
		usage()
		os.Exit(2)
	}

	if err := runCmd(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "glider-runtime:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: glider-runtime run --rootfs <path> [--hostname <name>] -- <cmd> [args...]")
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	rootfs := fs.String("rootfs", "", "path to a pre-existing root filesystem directory (required)")
	hostname := fs.String("hostname", "glider", "hostname to set inside the container's UTS namespace")
	stateDir := fs.String("state-dir", "", "override the default state directory (/var/lib/glider/containers); primarily for tests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	argv := fs.Args()
	if *rootfs == "" {
		usage()
		return fmt.Errorf("--rootfs is required")
	}
	if len(argv) == 0 {
		usage()
		return fmt.Errorf("no workload command specified after --")
	}

	id, err := process.NewContainerID()
	if err != nil {
		return err
	}

	cfg := process.Config{
		RootFS:      *rootfs,
		Argv:        argv,
		Hostname:    *hostname,
		StateDir:    *stateDir,
		ContainerID: id,
	}

	// SIGTERM/SIGINT delivered to glider-runtime itself (e.g. an operator
	// or test sending it to this process, matching runtime.md §6 exit-gate
	// (d)) become a stop request forwarded into the container by
	// process.Run — see its doc comment for the PID 1 signal-delivery
	// caveat this depends on.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	exitCode, err := process.Run(ctx, cfg)
	if err != nil {
		return err
	}
	os.Exit(exitCode)
	return nil
}
