package scheduler

import (
	"errors"
	"testing"

	"github.com/santinomarial/glider/internal/api"
)

func node(id string, cpu, used int64) api.Node {
	return api.Node{Metadata: api.Metadata{ID: id}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: cpu, MemoryBytes: 1024}}, Status: api.NodeStatus{Phase: api.NodeReady, Reserved: api.Resources{CPUMilli: used}}}
}
func task(cpu int64) api.Task {
	return api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: cpu, MemoryBytes: 1}}}
}
func TestScheduleFiltersCapacityAndScores(t *testing.T) {
	got, err := Schedule(task(500), []api.Node{node("full", 500, 500), node("busy", 1000, 400), node("free", 1000, 0)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node.Metadata.ID != "free" {
		t.Fatalf("picked %s", got.Node.Metadata.ID)
	}
}
func TestScheduleDeterministicTieBreak(t *testing.T) {
	got, _ := Schedule(task(1), []api.Node{node("b", 1000, 0), node("a", 1000, 0)}, nil)
	if got.Node.Metadata.ID != "a" {
		t.Fatalf("picked %s", got.Node.Metadata.ID)
	}
}
func TestScheduleSelectorAndReadiness(t *testing.T) {
	a := node("a", 1000, 0)
	a.Status.Phase = api.NodeSuspect
	b := node("b", 1000, 0)
	b.Spec.Labels = map[string]string{"zone": "west"}
	x := task(1)
	x.Spec.NodeSelector = map[string]string{"zone": "east"}
	_, err := Schedule(x, []api.Node{a, b}, nil)
	if !errors.Is(err, ErrUnschedulable) {
		t.Fatalf("error=%v", err)
	}
}
func TestReplicaSpreadPenalty(t *testing.T) {
	nodes := []api.Node{node("a", 1000, 0), node("b", 1000, 0)}
	assigned := []api.Assignment{{NodeID: "a"}, {NodeID: "a"}}
	got, _ := Schedule(task(1), nodes, assigned)
	if got.Node.Metadata.ID != "b" {
		t.Fatalf("picked %s", got.Node.Metadata.ID)
	}
}
