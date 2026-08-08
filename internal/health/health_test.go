package health

import (
	"github.com/santinomarial/glider/internal/api"
	"testing"
	"time"
)

func TestStartupAndReadinessThresholds(t *testing.T) {
	s := State{}
	r := Evaluate(s, "startup", false, api.Probe{FailureThreshold: 2})
	if r.Restart {
		t.Fatal("restarted too early")
	}
	r = Evaluate(r.State, "startup", false, api.Probe{FailureThreshold: 2})
	if !r.Restart {
		t.Fatal("startup threshold did not restart")
	}
	s = State{StartupComplete: true}
	r = Evaluate(s, "readiness", true, api.Probe{SuccessThreshold: 2})
	if r.State.Ready {
		t.Fatal("ready too early")
	}
	r = Evaluate(r.State, "readiness", true, api.Probe{SuccessThreshold: 2})
	if !r.State.Ready || !r.ReadinessChanged {
		t.Fatal("did not become ready")
	}
}
func TestRestartPolicyAndBoundedBackoff(t *testing.T) {
	if ShouldRestart(api.RestartNever, 1, true) {
		t.Fatal("Never restarted")
	}
	if !ShouldRestart(api.RestartOnFailure, 1, false) || ShouldRestart(api.RestartOnFailure, 0, false) {
		t.Fatal("OnFailure semantics")
	}
	if got := RestartBackoff(20, time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("backoff=%s", got)
	}
}
