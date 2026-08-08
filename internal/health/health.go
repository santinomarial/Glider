// Package health implements distinct startup, liveness, and readiness probe
// state machines. Probe execution is injected so exec probes can run inside a
// container namespace rather than accidentally on the host.
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/santinomarial/glider/internal/api"
)

type ExecRunner interface {
	Exec(context.Context, []string) error
}
type Prober struct {
	Client *http.Client
	Dialer net.Dialer
	Exec   ExecRunner
}

func (p *Prober) Check(ctx context.Context, probe api.Probe) error {
	timeout := probe.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch probe.Kind {
	case api.ProbeHTTP:
		client := p.Client
		if client == nil {
			client = &http.Client{Timeout: timeout}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP probe status %d", resp.StatusCode)
		}
		return nil
	case api.ProbeTCP:
		conn, err := p.Dialer.DialContext(ctx, "tcp", probe.Address)
		if err == nil {
			err = conn.Close()
		}
		return err
	case api.ProbeExec:
		if p.Exec == nil {
			return errors.New("container exec runner is required")
		}
		if len(probe.Command) == 0 {
			return errors.New("exec command is required")
		}
		return p.Exec.Exec(ctx, probe.Command)
	default:
		return fmt.Errorf("unsupported probe kind %q", probe.Kind)
	}
}

type State struct {
	StartupComplete    bool
	Ready              bool
	StartupFailures    int
	LivenessFailures   int
	ReadinessFailures  int
	ReadinessSuccesses int
}
type Result struct {
	State            State
	Restart          bool
	ReadinessChanged bool
}

func Evaluate(state State, kind string, success bool, probe api.Probe) Result {
	failure := probe.FailureThreshold
	if failure <= 0 {
		failure = 3
	}
	successes := probe.SuccessThreshold
	if successes <= 0 {
		successes = 1
	}
	before := state.Ready
	result := Result{State: state}
	switch kind {
	case "startup":
		if success {
			result.State.StartupComplete = true
			result.State.StartupFailures = 0
		} else {
			result.State.StartupFailures++
			result.Restart = result.State.StartupFailures >= failure
		}
	case "liveness":
		if success {
			result.State.LivenessFailures = 0
		} else {
			result.State.LivenessFailures++
			result.Restart = result.State.LivenessFailures >= failure
		}
	case "readiness":
		if success {
			result.State.ReadinessFailures = 0
			result.State.ReadinessSuccesses++
			if result.State.ReadinessSuccesses >= successes {
				result.State.Ready = true
			}
		} else {
			result.State.ReadinessSuccesses = 0
			result.State.ReadinessFailures++
			if result.State.ReadinessFailures >= failure {
				result.State.Ready = false
			}
		}
	}
	result.ReadinessChanged = before != result.State.Ready
	return result
}
func RestartBackoff(restarts int, base, maximum time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if maximum < base {
		maximum = time.Minute
	}
	delay := base
	for i := 0; i < restarts && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}
func ShouldRestart(policy api.RestartPolicy, exitCode int, livenessFailed bool) bool {
	if livenessFailed {
		return policy != api.RestartNever
	}
	switch policy {
	case api.RestartAlways:
		return true
	case api.RestartOnFailure:
		return exitCode != 0
	default:
		return false
	}
}
