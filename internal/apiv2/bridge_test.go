package apiv2

import (
	"testing"
	"time"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
)

func TestBridgePreservesResourceSemanticsAndUserMaps(t *testing.T) {
	input := api.Task{
		Metadata: api.Metadata{ID: "task", Revision: 17, Generation: 4},
		Spec: api.TaskSpec{
			Image:         "registry.example/app@sha256:abc",
			Resources:     api.Resources{CPUMilli: 250, MemoryBytes: 1024},
			Labels:        map[string]string{"phase": "production", "restart_policy": "owned-by-user"},
			NodeSelector:  map[string]string{"kind": "worker"},
			RestartPolicy: api.RestartOnFailure,
			Health:        api.HealthSpec{Readiness: &api.Probe{Kind: api.ProbeTCP, InitialDelay: 3 * time.Second}},
		},
		Status: api.TaskStatus{Phase: api.TaskRunning, AssignmentGeneration: 4},
	}
	request := new(gliderv2.PutTaskRequest)
	if err := FromLegacy(map[string]any{"task": input}, request); err != nil {
		t.Fatal(err)
	}
	task := request.GetTask()
	if task.GetMetadata().GetRevision() != 17 || task.GetSpec().GetRestartPolicy() != gliderv2.RestartPolicy_RESTART_POLICY_ON_FAILURE || task.GetSpec().GetHealth().GetReadiness().GetInitialDelay().AsDuration() != 3*time.Second || task.GetSpec().GetLabels()["phase"] != "production" || task.GetSpec().GetNodeSelector()["kind"] != "worker" {
		t.Fatalf("typed task = %+v", task)
	}
	output, err := ToLegacy(request)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip gliderv2.PutTaskRequest
	if err := FromLegacy(output, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.GetTask().GetSpec().GetLabels()["restart_policy"] != "owned-by-user" {
		t.Fatalf("round trip = %+v", roundTrip.GetTask())
	}
}

func TestBridgeRejectsUnknownFields(t *testing.T) {
	if err := FromLegacy(map[string]any{"id": "task", "unexpected": true}, new(gliderv2.GetTaskRequest)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
