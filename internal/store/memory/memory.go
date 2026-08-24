// Package memory is a race-safe semantic reference for the etcd transaction
// implementation and deterministic controller tests.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/store"
)

type Store struct {
	mu          sync.Mutex
	revision    int64
	tasks       map[string]api.Task
	nodes       map[string]api.Node
	assignments map[string]api.Assignment
}

func New() *Store {
	return &Store{tasks: map[string]api.Task{}, nodes: map[string]api.Node{}, assignments: map[string]api.Assignment{}}
}
func (s *Store) next() int64 { s.revision++; return s.revision }
func (s *Store) PutTask(t api.Task) api.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.APIVersion = api.Version
	t.Metadata.Revision = s.next()
	s.tasks[t.Metadata.ID] = t
	return t
}
func (s *Store) PutNode(n api.Node) api.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	n.APIVersion = api.Version
	n.Metadata.Revision = s.next()
	s.nodes[n.Metadata.ID] = n
	return n
}
func (s *Store) GetTask(_ context.Context, id string) (api.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, nil
}
func (s *Store) ListNodes(_ context.Context) ([]api.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	return out, nil
}
func (s *Store) ListAssignments(_ context.Context) ([]api.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.Assignment, 0, len(s.assignments))
	for _, a := range s.assignments {
		out = append(out, a)
	}
	return out, nil
}
func (s *Store) Bind(_ context.Context, r store.BindRequest) (api.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[r.TaskID]
	if !ok {
		return api.Assignment{}, store.ErrNotFound
	}
	n, ok := s.nodes[r.NodeID]
	if !ok {
		return api.Assignment{}, store.ErrNotFound
	}
	if t.Metadata.Revision != r.TaskRevision || n.Metadata.Revision != r.NodeRevision {
		return api.Assignment{}, store.ErrConflict
	}
	if _, ok := s.assignments[r.TaskID]; ok {
		return api.Assignment{}, store.ErrAlreadyAssigned
	}
	if t.Status.Phase != api.TaskPending {
		return api.Assignment{}, fmt.Errorf("%w: task phase %s", store.ErrConflict, t.Status.Phase)
	}
	if n.Status.Phase != api.NodeReady || n.Spec.Unschedulable || !n.Available().Fits(t.Spec.Resources) {
		return api.Assignment{}, store.ErrInsufficientCapacity
	}
	gen := t.Metadata.Generation + 1
	a := api.Assignment{APIVersion: api.Version, Metadata: api.Metadata{ID: t.Metadata.ID + "/" + fmt.Sprint(gen), Generation: gen}, TaskID: t.Metadata.ID, WorkloadID: t.Spec.WorkloadID, NodeID: n.Metadata.ID, Generation: gen, Resources: t.Spec.Resources, Image: t.Spec.Image, Command: append([]string(nil), t.Spec.Command...), RestartPolicy: t.Spec.RestartPolicy, Health: t.Spec.Health, HostPorts: append([]uint16(nil), t.Spec.HostPorts...), NetworkPolicy: t.Spec.NetworkPolicy, CreatedAt: time.Now().UTC()}
	t.Status.Phase = api.TaskScheduled
	t.Status.NodeID = n.Metadata.ID
	t.Status.AssignmentGeneration = gen
	t.Status.Ready = false
	t.Status.StartedAt = time.Time{}
	t.Status.FinishedAt = time.Time{}
	t.Status.ExitCode = nil
	t.Status.TerminationReason = ""
	t.Metadata.Generation = gen
	t.Metadata.Revision = s.next()
	n.Status.Reserved = n.Status.Reserved.Add(t.Spec.Resources)
	n.Metadata.Revision = s.next()
	a.Metadata.Revision = s.next()
	s.tasks[t.Metadata.ID] = t
	s.nodes[n.Metadata.ID] = n
	s.assignments[t.Metadata.ID] = a
	return a, nil
}

func (s *Store) ReportTaskRunning(_ context.Context, taskID string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return store.ErrNotFound
	}
	a, ok := s.assignments[taskID]
	if !ok || a.Generation != generation || t.Status.AssignmentGeneration != generation {
		return store.ErrConflict
	}
	if t.Status.Phase == api.TaskRunning {
		return nil
	}
	if t.Status.Phase != api.TaskScheduled {
		return store.ErrConflict
	}
	t.Status.Phase = api.TaskRunning
	if t.Status.StartedAt.IsZero() {
		t.Status.StartedAt = time.Now().UTC()
	}
	t.Metadata.Revision = s.next()
	s.tasks[taskID] = t
	return nil
}

func (s *Store) CompleteTask(_ context.Context, taskID string, generation int64, exitCode *int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return store.ErrNotFound
	}
	if t.Status.AssignmentGeneration != generation {
		return store.ErrConflict
	}
	if t.Status.Phase == api.TaskTerminated {
		return nil
	}
	a, ok := s.assignments[taskID]
	if !ok || a.Generation != generation {
		return store.ErrConflict
	}
	n, ok := s.nodes[a.NodeID]
	if !ok {
		return store.ErrNotFound
	}
	n.Status.Reserved = n.Status.Reserved.Sub(a.Resources)
	if n.Status.Reserved.CPUMilli < 0 || n.Status.Reserved.MemoryBytes < 0 {
		return fmt.Errorf("corrupt negative node reservation")
	}
	t.Status.Phase = api.TaskTerminated
	t.Status.Address = ""
	t.Status.Ready = false
	t.Status.FinishedAt = time.Now().UTC()
	if exitCode != nil {
		code := *exitCode
		t.Status.ExitCode = &code
	}
	t.Status.TerminationReason = reason
	t.Metadata.Revision = s.next()
	n.Metadata.Revision = s.next()
	s.tasks[taskID] = t
	s.nodes[a.NodeID] = n
	delete(s.assignments, taskID)
	return nil
}

func (s *Store) RestartTask(_ context.Context, taskID string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return store.ErrNotFound
	}
	a, ok := s.assignments[taskID]
	if !ok || a.Generation != generation || t.Status.AssignmentGeneration != generation {
		return store.ErrConflict
	}
	n, ok := s.nodes[a.NodeID]
	if !ok {
		return store.ErrNotFound
	}
	n.Status.Reserved = n.Status.Reserved.Sub(a.Resources)
	if n.Status.Reserved.CPUMilli < 0 || n.Status.Reserved.MemoryBytes < 0 {
		return fmt.Errorf("corrupt negative node reservation")
	}
	t.Status.Phase = api.TaskPending
	t.Status.NodeID = ""
	t.Status.Address = ""
	t.Status.Ready = false
	t.Status.RestartCount++
	t.Status.StartedAt = time.Time{}
	t.Status.FinishedAt = time.Time{}
	t.Status.ExitCode = nil
	t.Status.TerminationReason = ""
	t.Metadata.Revision = s.next()
	n.Metadata.Revision = s.next()
	s.tasks[taskID] = t
	s.nodes[a.NodeID] = n
	delete(s.assignments, taskID)
	return nil
}
