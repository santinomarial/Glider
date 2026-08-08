package workload

import (
	"context"
	"testing"
	"time"

	"github.com/santinomarial/glider/internal/api"
)

type fakeStore struct {
	revision  int64
	workloads map[string]api.Workload
	tasks     map[string]api.Task
}

func (f *fakeStore) next() int64 { f.revision++; return f.revision }
func (f *fakeStore) ListWorkloads(context.Context) ([]api.Workload, error) {
	var out []api.Workload
	for _, v := range f.workloads {
		out = append(out, v)
	}
	return out, nil
}
func (f *fakeStore) PutWorkload(_ context.Context, w api.Workload, _ int64) (api.Workload, error) {
	w.Metadata.Revision = f.next()
	f.workloads[w.Metadata.ID] = w
	return w, nil
}
func (f *fakeStore) ListTasks(context.Context) ([]api.Task, error) {
	var out []api.Task
	for _, v := range f.tasks {
		out = append(out, v)
	}
	return out, nil
}
func (f *fakeStore) PutTask(_ context.Context, v api.Task, _ int64) (api.Task, error) {
	v.Metadata.Revision = f.next()
	f.tasks[v.Metadata.ID] = v
	return v, nil
}
func (f *fakeStore) DeleteTask(_ context.Context, id string, _ int64) error {
	delete(f.tasks, id)
	return nil
}
func TestReplicaScaleUpAndDownDeterministically(t *testing.T) {
	s := &fakeStore{workloads: map[string]api.Workload{}, tasks: map[string]api.Task{}}
	w := api.Workload{Metadata: api.Metadata{ID: "api", Revision: 1}, Spec: api.WorkloadSpec{Replicas: 3, Template: api.TaskSpec{Image: "example/app"}}}
	s.workloads[w.Metadata.ID] = w
	c, _ := New(s)
	if err := c.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	revision, _ := templateRevision(w.Spec.Template)
	for _, id := range []string{"api-" + revision[:8] + "-000000", "api-" + revision[:8] + "-000001", "api-" + revision[:8] + "-000002"} {
		if _, ok := s.tasks[id]; !ok {
			t.Fatalf("missing %s", id)
		}
	}
	w = s.workloads["api"]
	w.Spec.Replicas = 1
	if err := c.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if len(s.tasks) != 1 {
		t.Fatalf("tasks=%d", len(s.tasks))
	}
	if _, ok := s.tasks["api-"+revision[:8]+"-000000"]; !ok {
		t.Fatal("scale down did not retain oldest replica")
	}
}

func TestRollingUpdateWaitsForReadyReplacement(t *testing.T) {
	s := &fakeStore{workloads: map[string]api.Workload{}, tasks: map[string]api.Task{}}
	now := time.Unix(1000, 0)
	c, _ := New(s)
	c.now = func() time.Time { return now }
	w := api.Workload{Metadata: api.Metadata{ID: "api", Revision: 1}, Spec: api.WorkloadSpec{Replicas: 2, Template: api.TaskSpec{Image: "app:v1"}, Rollout: api.RolloutStrategy{MaxSurge: 1, MaxUnavailable: 0, ProgressDeadline: time.Minute}}}
	s.workloads["api"] = w
	if err := c.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	for id, task := range s.tasks {
		task.Status.Ready = true
		s.tasks[id] = task
	}
	oldRevision, _ := templateRevision(w.Spec.Template)
	w = s.workloads["api"]
	w.Spec.Template.Image = "app:v2"
	if err := c.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if len(s.tasks) != 3 {
		t.Fatalf("surge tasks=%d", len(s.tasks))
	}
	oldCount := 0
	var replacement string
	for id, task := range s.tasks {
		if task.Spec.TemplateRevision == oldRevision {
			oldCount++
		} else {
			replacement = id
		}
	}
	if oldCount != 2 {
		t.Fatalf("old replicas removed before replacement readiness: %d", oldCount)
	}
	task := s.tasks[replacement]
	task.Status.Ready = true
	s.tasks[replacement] = task
	w = s.workloads["api"]
	if err := c.Reconcile(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	oldCount = 0
	for _, task := range s.tasks {
		if task.Spec.TemplateRevision == oldRevision {
			oldCount++
		}
	}
	if oldCount != 1 {
		t.Fatalf("old replicas after ready surge=%d", oldCount)
	}
}

func TestBadRolloutStallsWithoutDestroyingHealthyCapacity(t *testing.T) {
	s := &fakeStore{workloads: map[string]api.Workload{}, tasks: map[string]api.Task{}}
	now := time.Unix(2000, 0)
	c, _ := New(s)
	c.now = func() time.Time { return now }
	w := api.Workload{Metadata: api.Metadata{ID: "api", Revision: 1}, Spec: api.WorkloadSpec{Replicas: 2, Template: api.TaskSpec{Image: "v1"}, Rollout: api.RolloutStrategy{MaxSurge: 1, ProgressDeadline: time.Minute}}}
	s.workloads["api"] = w
	_ = c.Reconcile(context.Background(), w)
	for id, task := range s.tasks {
		task.Status.Ready = true
		s.tasks[id] = task
	}
	w = s.workloads["api"]
	w.Spec.Template.Image = "broken"
	_ = c.Reconcile(context.Background(), w)
	now = now.Add(2 * time.Minute)
	w = s.workloads["api"]
	_ = c.Reconcile(context.Background(), w)
	if len(s.tasks) != 3 {
		t.Fatalf("healthy capacity destroyed: tasks=%d", len(s.tasks))
	}
	if got := s.workloads["api"].Status.RolloutPhase; got != "Stalled" {
		t.Fatalf("phase=%s", got)
	}
}
