// Command glider-test-helper is the reproducible workload fixture for
// glider-runtime's integration tests. It is built from source for every
// test run (see runtime_integration_test.go) rather than relying on an
// arbitrary developer machine's /bin/sh, and is statically linked
// (CGO_ENABLED=0) so it has no dynamic library dependencies for a bare
// test rootfs to satisfy — runtime.md §6 Phase 1 explicitly does no image
// handling, so the fixture rootfs is just this one binary.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Minimize this process's own OS-thread footprint: under a tight
	// pids.max (pid-hog's whole point), the Go runtime creating its own
	// scheduler/GC threads competes for the same task-count budget as
	// this fixture's own forked children, and Go's runtime treats a
	// failed thread creation as an unrecoverable fatal error, not a
	// catchable Go error — see cmdPIDHog's doc comment.
	runtime.GOMAXPROCS(1)

	if len(os.Args) < 2 {
		usage()
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
	case "sleep-default":
		cmdSleepDefault()
	case "ignore-term":
		cmdIgnoreTerm()
	case "zombie-churn":
		cmdZombieChurn()
	case "orphan-churn":
		cmdOrphanChurn()
	case "cpu-spin":
		cmdCPUSpin()
	case "mem-touch-and-hold":
		cmdMemTouchAndHold()
	case "mem-hog":
		cmdMemHog()
	case "pid-hog":
		cmdPIDHog()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: glider-test-helper <hostname|pid|procs|exit|trap-term|write|stat|sleep-default|ignore-term|zombie-churn|orphan-churn> [args...]")
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
// this process. Phase 2: glider-init is PID 1 and the workload is a
// supervised child (docs/adr/0006), so with nothing else spawned this is
// ["1", <own pid>], not just ["1"] as it was in Phase 1's direct-exec model.
func cmdProcs() {
	fmt.Println(strings.Join(listProcPIDs(), ","))
}

func listProcPIDs() []string {
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
	out := make([]string, len(pids))
	for i, p := range pids {
		out[i] = strconv.Itoa(p)
	}
	return out
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

// cmdTrapTerm proves signal delivery reaches the workload: it explicitly
// traps SIGTERM (a well-behaved container entrypoint would too — this is
// exactly why real init systems like tini exist) and records that it was
// caught.
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

// cmdSleepDefault installs no signal handling at all and just sleeps.
// Under Phase 2's supervisor model the workload is PID 2+, not PID 1
// (docs/adr/0006), so it receives SIGTERM with the kernel's normal default
// disposition (terminate) — unlike Phase 1, where an unhandled SIGTERM
// sent to a namespace's PID 1 is not delivered with default disposition at
// all. This fixture exists specifically to prove that a workload which
// does *nothing* special still stops correctly now.
func cmdSleepDefault() {
	secs := 30
	if len(os.Args) >= 3 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			secs = n
		}
	}
	time.Sleep(time.Duration(secs) * time.Second)
}

// cmdIgnoreTerm explicitly ignores SIGTERM (and SIGINT), so glider-init's
// forwarded graceful-termination signal has no effect — used to test
// grace-period-then-SIGKILL escalation (runtime.md §6 "graceful shutdown
// protocol").
func cmdIgnoreTerm() {
	marker := ""
	if len(os.Args) >= 3 {
		marker = os.Args[2]
	}
	if marker != "" {
		_ = os.WriteFile(marker, []byte("ready\n"), 0o644)
	}
	signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	time.Sleep(60 * time.Second)
}

// cmdZombieChurn forks n short-lived children that each exit almost
// immediately, then exits itself *without ever wait()-ing on any of them*
// — deliberately simulating a buggy/careless workload. This reparents up
// to n simultaneously-exiting (zombie-or-about-to-be) processes to
// glider-init (PID 1) all at once, exercising reapExited's WNOHANG-loop
// drain of *multiple* children in a single sweep, not just the case of
// reaping one at a time. The caller (integration test) verifies the
// result via the standard host-side leaked-process check — if
// glider-init's reaping is broken, some of these n children would survive
// as zombies/leaked processes past container teardown.
func cmdZombieChurn() {
	const n = 20
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for i := 0; i < n; i++ {
		c := exec.Command(self, "exit", "0")
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			os.Exit(1)
		}
		// Deliberately not waited on.
	}
	fmt.Println("SPAWNED")
	// Exit immediately: reparents all n children (exited or about to be)
	// to glider-init in one burst.
}

// cmdOrphanChurn forks a child which itself forks a grandchild (that
// sleeps, simulating a long-lived process) and then the child exits
// immediately — orphaning the grandchild, which the kernel reparents to
// PID 1 (glider-init). This process (the main workload) waits on its own
// direct child only (correctly avoiding creating a zombie of its own),
// then polls (bounded, no fixed sleep-and-hope) the grandchild's
// /proc/<pid>/stat PPid field until it reads 1, proving reparenting to
// glider-init occurred, and prints REPARENTED or TIMEOUT accordingly. It
// exits without waiting for the grandchild — proving glider-init doesn't
// require the workload to do so, and that nothing leaks once the
// container itself tears down (checked host-side by the caller).
func cmdOrphanChurn() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Spawn the child via a small shell-free chain: this binary re-exec'd
	// with a hidden subcommand that forks a grandchild sleeper, then exits.
	// Output() both starts and waits for the child, and prints its stdout
	// (the grandchild's PID, written before the child exits).
	child := exec.Command(self, "__orphan_child__")
	out, err := child.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child failed:", err)
		os.Exit(1)
	}
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse grandchild pid:", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ppid, err := readPPid(grandchildPID)
		if err == nil && ppid == 1 {
			fmt.Println("REPARENTED")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Println("TIMEOUT")
}

// __orphan_child__ is invoked only by cmdOrphanChurn (not listed in
// usage() — an internal fork target, analogous in spirit to glider-init's
// own __glider_init__ re-exec argument).
func runOrphanChild() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	grandchild := exec.Command(self, "sleep-default", "5")
	if err := grandchild.Start(); err != nil {
		os.Exit(1)
	}
	fmt.Println(grandchild.Process.Pid)
	// Deliberately exit without waiting — orphans the grandchild.
}

func readPPid(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, fmt.Errorf("unexpected stat format")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected stat field count")
	}
	return strconv.Atoi(fields[1]) // ppid is field 4 overall, index 1 after comm+state
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

func init() {
	if len(os.Args) >= 2 && os.Args[1] == "__orphan_child__" {
		runOrphanChild()
		os.Exit(0)
	}
}

// cmdCPUSpin busy-loops on pure CPU work (no syscalls, nothing the Go
// runtime/scheduler could satisfy by descheduling this goroutine onto an
// idle OS thread instead of actually consuming CPU time) for the given
// number of seconds — used to prove a configured cpu.max quota is
// actually enforced (Phase 4 §30), via the caller reading cpu.stat's
// nr_throttled/throttled_usec counters while this runs. GOMAXPROCS is
// pinned to 1 so the whole process's CPU demand is deterministic and
// doesn't itself depend on how many host CPUs are visible.
func cmdCPUSpin() {
	secs := 5
	if len(os.Args) >= 3 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			secs = n
		}
	}
	runtime.GOMAXPROCS(1)
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	var x uint64 = 1
	for time.Now().Before(deadline) {
		for i := 0; i < 10_000_000; i++ {
			x = x*1103515245 + 12345
		}
	}
	// Prevent the compiler from proving x is unused and eliding the loop.
	if x == 0 {
		fmt.Println("unreachable")
	}
}

// cmdMemTouchAndHold allocates and touches (forces real RSS for, not just
// a virtual mapping) sizeMiB mebibytes, then holds that allocation for
// holdSecs seconds — a stable, non-racy window for the caller to read
// memory.current and confirm it reflects real usage (Phase 4 §31), as
// opposed to a workload that touches memory and immediately exits before
// anyone can observe it.
func cmdMemTouchAndHold() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: glider-test-helper mem-touch-and-hold <size-mib> <hold-seconds>")
		os.Exit(2)
	}
	sizeMiB, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	holdSecs, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	buf := make([]byte, sizeMiB*1024*1024)
	const pageSize = 4096
	for i := 0; i < len(buf); i += pageSize {
		buf[i] = 1
	}
	fmt.Println("TOUCHED")
	time.Sleep(time.Duration(holdSecs) * time.Second)
	_ = buf[len(buf)-1]
}

// cmdMemHog aggressively and indefinitely grows an allocation, touching
// every page, never voluntarily stopping — used to prove a configured
// memory.max hard-contains a genuinely runaway workload (Phase 4 §31):
// the expectation is the kernel OOM-kills this process (SIGKILL) rather
// than letting it grow without bound, and that the host/runtime remain
// healthy afterward. Grows in small increments (not one giant allocation)
// so the container is killed promptly once it crosses the configured
// limit, rather than after one large overshoot.
func cmdMemHog() {
	const chunk = 4 * 1024 * 1024 // 4 MiB per step
	const pageSize = 4096
	var held [][]byte
	for {
		b := make([]byte, chunk)
		for i := 0; i < len(b); i += pageSize {
			b[i] = 1
		}
		held = append(held, b)
	}
}

// cmdPIDHog repeatedly forks short-lived children as fast as possible for
// the given duration, counting successes and failures, then reports
// "SPAWNED=<n> REFUSED=<n>" — proof a configured pids.max both allows
// forking up to the limit and refuses forks beyond it (Phase 4 §32),
// without the container ever exceeding the configured population. Each
// child sleeps briefly (rather than exiting instantly) so the caller has
// a real window to observe pids.current sitting near the configured
// limit, not just a final refusal count.
func cmdPIDHog() {
	secs := 3
	if len(os.Args) >= 3 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			secs = n
		}
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var spawned, refused int
	var children []*exec.Cmd
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		c := exec.Command(self, "sleep-default", "2")
		if err := c.Start(); err != nil {
			refused++
			time.Sleep(2 * time.Millisecond)
			continue
		}
		spawned++
		children = append(children, c)
		// A small pace between successful forks too, not just refusals:
		// exec.Cmd.Start (via the Go runtime's own forkAndExecInChild)
		// can itself need to spin up a fresh OS thread as part of
		// handling the fork/exec syscalls; slamming pids.max with a
		// tight burst risks that thread-creation attempt itself being
		// refused by the very same limit, which the Go runtime treats as
		// an unrecoverable fatal error (not a catchable Start() error) —
		// this fixture's own process is subject to the same pids.max as
		// the children it spawns (Phase 4 §12), so it must leave itself
		// enough headroom to keep scheduling normally right up to the
		// limit, not just enough for the *next fork* to be refused
		// gracefully.
		time.Sleep(time.Millisecond)
	}
	for _, c := range children {
		_ = c.Wait()
	}
	fmt.Printf("SPAWNED=%d REFUSED=%d\n", spawned, refused)
}
