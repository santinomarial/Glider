package controlplane

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/transport"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestNodeIdentityCannotImpersonatePeer(t *testing.T) {
	node := transport.Principal{Name: "node-a", Roles: map[string]bool{"node": true}}
	if !nodeCanMutate(node, "node-a") {
		t.Fatal("node denied itself")
	}
	if nodeCanMutate(node, "node-b") {
		t.Fatal("node allowed to impersonate peer")
	}
	admin := transport.Principal{Name: "admin", Roles: map[string]bool{"admin": true}}
	if !nodeCanMutate(admin, "node-b") {
		t.Fatal("non-node administrative principal denied")
	}
}

func TestRequiredRevisionRejectsMissingFractionalAndNonPositive(t *testing.T) {
	for _, value := range []any{nil, 0.0, -1.0, 1.5, "1"} {
		request, _ := structpb.NewStruct(map[string]any{"revision": value})
		if _, err := requiredRevision(request, "revision"); err == nil {
			t.Fatalf("accepted revision %#v", value)
		}
	}
	request, _ := structpb.NewStruct(map[string]any{"revision": float64(42)})
	if got, err := requiredRevision(request, "revision"); err != nil || got != 42 {
		t.Fatalf("revision = %d, %v", got, err)
	}
}

func TestMutationRequiresBoundedIdempotencyKey(t *testing.T) {
	if err := requireIdempotencyKey(api.Metadata{}); err == nil {
		t.Fatal("empty idempotency key accepted")
	}
	if err := requireIdempotencyKey(api.Metadata{IdempotencyKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareTaskCreateRejectsForgedStatus(t *testing.T) {
	forged := api.Task{Status: api.TaskStatus{Phase: api.TaskRunning, NodeID: "node-a", Ready: true}}
	if _, err := prepareTaskMutation(forged, nil); err == nil {
		t.Fatal("forged running status accepted")
	}
	pending, err := prepareTaskMutation(api.Task{Status: api.TaskStatus{Phase: api.TaskPending}}, nil)
	if err != nil || pending.Status.Phase != api.TaskPending {
		t.Fatalf("pending create = %+v, %v", pending.Status, err)
	}
}

func TestPrepareTaskUpdatePreservesServerStatus(t *testing.T) {
	retryAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	current := api.Task{Metadata: api.Metadata{ID: "task", Revision: 4, Generation: 2}, Spec: api.TaskSpec{Image: "old"}, Status: api.TaskStatus{Phase: api.TaskPending, RestartCount: 3, RestartNotBefore: retryAt}}
	update := api.Task{Metadata: api.Metadata{ID: "task", Revision: 4}, Spec: api.TaskSpec{Image: "new"}}
	prepared, err := prepareTaskMutation(update, &current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Status, current.Status) || prepared.Metadata.Generation != current.Metadata.Generation {
		t.Fatalf("server state was not preserved: %+v", prepared)
	}
	update.Status = api.TaskStatus{Phase: api.TaskPending, Ready: true}
	if _, err := prepareTaskMutation(update, &current); err == nil {
		t.Fatal("forged readiness update accepted")
	}
}

func TestPrepareTaskUpdateRejectsOwnedOrActiveTask(t *testing.T) {
	for _, current := range []api.Task{
		{Status: api.TaskStatus{Phase: api.TaskRunning}},
		{Status: api.TaskStatus{Phase: api.TaskPending}, Spec: api.TaskSpec{WorkloadID: "deployment"}},
	} {
		update := api.Task{Metadata: api.Metadata{Revision: 1}}
		if _, err := prepareTaskMutation(update, &current); !errors.Is(err, errTaskNotMutable) {
			t.Fatalf("mutable task accepted: %+v, %v", current, err)
		}
	}
}

func TestPrepareNodeCreateOwnsPhaseAndReservation(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	forged := api.Node{Status: api.NodeStatus{Phase: api.NodeReady, Reserved: api.Resources{CPUMilli: 100}}}
	if _, err := prepareNodeMutation(forged, nil, true, now); err == nil {
		t.Fatal("forged ready node accepted")
	}
	created, err := prepareNodeMutation(api.Node{Status: api.NodeStatus{ObservedUsage: api.Resources{CPUMilli: 20}}}, nil, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status.Phase != api.NodeJoining || created.Status.Reserved != (api.Resources{}) || !created.Status.UpdatedAt.Equal(now) {
		t.Fatalf("created status=%+v", created.Status)
	}
}

func TestPrepareNodeUpdatePreservesAuthorityFields(t *testing.T) {
	previous := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	now := previous.Add(time.Hour)
	current := api.Node{Metadata: api.Metadata{Generation: 3}, Spec: api.NodeSpec{Unschedulable: true}, Status: api.NodeStatus{Phase: api.NodeDraining, Reserved: api.Resources{CPUMilli: 500}, ObservedUsage: api.Resources{CPUMilli: 100}, UpdatedAt: previous}}
	update := api.Node{Status: api.NodeStatus{Images: []string{"sha256:image"}}}
	prepared, err := prepareNodeMutation(update, &current, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status.Phase != api.NodeDraining || prepared.Status.Reserved.CPUMilli != 500 || !prepared.Spec.Unschedulable || !prepared.Status.UpdatedAt.Equal(now) {
		t.Fatalf("authority fields changed: %+v", prepared)
	}
	forged := update
	forged.Status.Reserved.CPUMilli = 1
	if _, err := prepareNodeMutation(forged, &current, true, now); err == nil {
		t.Fatal("forged reservation accepted")
	}
}

func TestSecretDeliveryRequiresExactNodeAndGeneration(t *testing.T) {
	assignment := api.Assignment{TaskID: "task", NodeID: "node-a", Generation: 7}
	node := transport.Principal{Name: "node-a", Roles: map[string]bool{"node": true}}
	if !nodeOwnsAssignment(node, assignment, 7) {
		t.Fatal("current owner generation denied")
	}
	if nodeOwnsAssignment(node, assignment, 6) {
		t.Fatal("stale generation accepted")
	}
	peer := transport.Principal{Name: "node-b", Roles: map[string]bool{"node": true}}
	if nodeOwnsAssignment(peer, assignment, 7) {
		t.Fatal("peer node accepted")
	}
	operator := transport.Principal{Name: "operator", Roles: map[string]bool{"operator": true}}
	if nodeOwnsAssignment(operator, assignment, 7) {
		t.Fatal("non-node principal accepted")
	}
}
