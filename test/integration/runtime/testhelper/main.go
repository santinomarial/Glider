// Command glider-test-helper is the reproducible workload fixture for
// glider-runtime's Phase 1 integration tests. It is built from source for
// every test run (see runtime_integration_test.go) rather than relying on
// an arbitrary developer machine's /bin/sh, and is statically linked
// (CGO_ENABLED=0) so it has no dynamic library dependencies for a bare
// test rootfs to satisfy — runtime.md §6 Phase 1 explicitly does no image
// handling, so the fixture rootfs is just this one binary.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper <hostname|pid|procs|exit|trap-term|write|stat> [args...]")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "hostname":
		cmdHostname()
	case "pid":
		fmt.Println(os.Getpid())
	case "procs":
		cmdProcs()
	case "exit":
		cmdExit()
	case "trap-term":
		cmdTrapTerm()
	case "write":
		cmdWrite()
	case "stat":
		cmdStat()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func cmdHostname() {
	h, err := os.Hostname()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(h)
}

// cmdProcs lists numeric entries under /proc — the set of PIDs visible to
// this process. Inside an isolated PID namespace with nothing else
// spawned, this is exactly ["1"], which is what the namespace-isolation
// integration test asserts.
func cmdProcs() {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var pids []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, n)
		}
	}
	sort.Ints(pids)
	for i, p := range pids {
		if i > 0 {
			fmt.Print(",")
		}
		fmt.Print(p)
	}
	fmt.Println()
}

func cmdExit() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper exit <code>")
		os.Exit(2)
	}
	code, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(code)
}

// cmdTrapTerm proves signal delivery across the namespace boundary
// honestly, per runtime.md §5's documented PID 1 caveat: an *unhandled*
// SIGTERM sent to a namespace's PID 1 is not delivered with default
// disposition by the kernel, so this fixture explicitly traps it (a
// well-behaved container entrypoint would too — this is exactly why real
// init systems like tini exist) rather than the test relying on a false
// guarantee that any arbitrary unmodified program would stop.
func cmdTrapTerm() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper trap-term <marker-path>")
		os.Exit(2)
	}
	marker := os.Args[2]

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)

	<-sigCh
	if err := os.WriteFile(marker, []byte("caught\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func cmdWrite() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper write <path> <content>")
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[2], []byte(os.Args[3]), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// cmdStat reports whether a path is visible from inside the container,
// used to prove pivot_root isolation from the container's own point of
// view (runtime.md §6 exit-gate (c)).
func cmdStat() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper stat <path>")
		os.Exit(2)
	}
	if _, err := os.Stat(os.Args[2]); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("MISSING")
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("EXISTS")
}
