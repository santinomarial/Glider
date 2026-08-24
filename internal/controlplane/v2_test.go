package controlplane

import (
	"testing"
	"time"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestV2TaskBridgePreservesTypedSemantics(t *testing.T) {
	started := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	request := &gliderv2.Task{
		Metadata: &gliderv2.Metadata{Id: "task", Revision: 17, Generation: 4, IdempotencyKey: "request"},
		Spec: &gliderv2.TaskSpec{
			Image:         "registry.example/app@sha256:abc",
			Resources:     &gliderv2.Resources{CpuMilli: 250, MemoryBytes: 1024},
			Labels:        map[string]string{"phase": "production", "restart_policy": "owned-by-user"},
			NodeSelector:  map[string]string{"kind": "worker"},
			RestartPolicy: gliderv2.RestartPolicy_RESTART_POLICY_ON_FAILURE,
			Health: &gliderv2.HealthSpec{Readiness: &gliderv2.Probe{
				Kind:         gliderv2.ProbeKind_PROBE_KIND_TCP,
				InitialDelay: durationpb.New(3 * time.Second),
				Period:       durationpb.New(5 * time.Second),
			}},
		},
		Status: &gliderv2.TaskStatus{Phase: gliderv2.TaskPhase_TASK_PHASE_RUNNING, AssignmentGeneration: 4, StartTime: timestamppb.New(started)},
	}
	encoded, err := typedResource(request)
	if err != nil {
		t.Fatal(err)
	}
	var internal api.Task
	if err := decode(encoded, &internal); err != nil {
		t.Fatal(err)
	}
	if internal.Metadata.Revision != 17 || internal.Spec.Resources.CPUMilli != 250 || internal.Spec.RestartPolicy != api.RestartOnFailure || internal.Spec.Health.Readiness.Kind != api.ProbeTCP || internal.Spec.Health.Readiness.InitialDelay != 3*time.Second || internal.Status.Phase != api.TaskRunning || !internal.Status.StartedAt.Equal(started) {
		t.Fatalf("internal task=%+v", internal)
	}

	exitCode := 23
	internal.Status.ExitCode = &exitCode
	internal.Status.TerminationReason = "workload exited"
	legacy, err := encode(internal, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := new(gliderv2.GetTaskResponse)
	if err := typedResponse(legacy, "task", response, nil); err != nil {
		t.Fatal(err)
	}
	if response.Task.GetMetadata().GetRevision() != 17 || response.Task.GetSpec().GetRestartPolicy() != gliderv2.RestartPolicy_RESTART_POLICY_ON_FAILURE || response.Task.GetSpec().GetLabels()["phase"] != "production" || response.Task.GetSpec().GetLabels()["restart_policy"] != "owned-by-user" || response.Task.GetSpec().GetNodeSelector()["kind"] != "worker" || response.Task.GetStatus().GetPhase() != gliderv2.TaskPhase_TASK_PHASE_RUNNING || response.Task.GetStatus().GetExitCode() != 23 || response.Task.GetStatus().GetTerminationReason() != "workload exited" {
		t.Fatalf("typed response=%+v", response.Task)
	}
}

func TestV2RegistrationExposesTypedService(t *testing.T) {
	server := grpc.NewServer()
	RegisterV2(server, nil)
	if _, ok := server.GetServiceInfo()["glider.v2.ControlPlaneService"]; !ok {
		t.Fatal("typed control-plane service was not registered")
	}
}

func TestV2ListOptionsFailClosedUntilPaginationIsImplemented(t *testing.T) {
	if err := validateListOptions(&gliderv2.ListOptions{PageSize: 10}); err == nil {
		t.Fatal("unsupported pagination was silently ignored")
	}
	if err := validateListOptions(nil); err != nil {
		t.Fatal(err)
	}
}
