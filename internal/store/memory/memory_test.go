package memory

import (
	"context"
	"errors"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/store"
	"sync"
	"testing"
)

func TestConcurrentBindHasOneWinner(t *testing.T) {
	s := New()
	task := s.PutTask(api.Task{Metadata: api.Metadata{ID: "t"}, Status: api.TaskStatus{Phase: api.TaskPending}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: 500}}})
	a := s.PutNode(api.Node{Metadata: api.Metadata{ID: "a"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000}}, Status: api.NodeStatus{Phase: api.NodeReady}})
	b := s.PutNode(api.Node{Metadata: api.Metadata{ID: "b"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000}}, Status: api.NodeStatus{Phase: api.NodeReady}})
	reqs := []store.BindRequest{{TaskID: "t", TaskRevision: task.Metadata.Revision, NodeID: "a", NodeRevision: a.Metadata.Revision}, {TaskID: "t", TaskRevision: task.Metadata.Revision, NodeID: "b", NodeRevision: b.Metadata.Revision}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, r := range reqs {
		wg.Add(1)
		go func(r store.BindRequest) { defer wg.Done(); _, err := s.Bind(context.Background(), r); errs <- err }(r)
	}
	wg.Wait()
	close(errs)
	wins, losses := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrAlreadyAssigned) {
			losses++
		} else {
			t.Errorf("unexpected %v", err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("wins=%d losses=%d", wins, losses)
	}
	assignments, _ := s.ListAssignments(context.Background())
	if len(assignments) != 1 {
		t.Fatalf("assignments=%d", len(assignments))
	}
}
