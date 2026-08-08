//go:build linux

package process

import (
	"fmt"
	"os"
	"syscall"

	"github.com/santinomarial/glider/internal/runtime/security"
)

// RunWorkload is the internal Phase 8 security trampoline. fd 3 carries a
// ready byte followed by either a failure message or EOF-on-success via
// CLOEXEC, allowing glider-init to preserve its exact exec confirmation.
func RunWorkload(args []string) {
	status := os.NewFile(fdWorkloadExecStatus, "workload-exec-status")
	if status == nil || !fdValid(fdWorkloadExecStatus) || len(args) < 2 {
		fmt.Fprintln(os.Stderr, "glider-runtime: __glider_workload__ is an internal entrypoint")
		os.Exit(2)
	}
	if err := setCloseOnExec(fdWorkloadExecStatus); err != nil { workloadFail(status, err) }
	if _, err := status.Write([]byte{1}); err != nil { os.Exit(2) }
	if err := security.ApplyDefault(); err != nil { workloadFail(status, err) }
	if err := syscall.Exec(args[0], args[1:], os.Environ()); err != nil { workloadFail(status, fmt.Errorf("exec workload: %w", err)) }
}

func workloadFail(status *os.File, err error) { _, _ = status.Write([]byte(err.Error())); _ = status.Close(); os.Exit(1) }
