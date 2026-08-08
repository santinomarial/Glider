package service

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"testing"
)

type fakeStore struct {
	services map[string]api.Service
	tasks    []api.Task
}

func (f *fakeStore) ListServices(context.Context) ([]api.Service, error) {
	var out []api.Service
	for _, v := range f.services {
		out = append(out, v)
	}
	return out, nil
}
func (f *fakeStore) ListTasks(context.Context) ([]api.Task, error) { return f.tasks, nil }
func (f *fakeStore) PutService(_ context.Context, s api.Service, _ int64) (api.Service, error) {
	f.services[s.Metadata.ID] = s
	return s, nil
}
func TestReconcilePublishesOnlyReadyMatchingValidEndpoints(t *testing.T) {
	svc := api.Service{Metadata: api.Metadata{ID: "api", Revision: 1}, Spec: api.ServiceSpec{Selector: map[string]string{"app": "api"}, Port: 80, TargetPort: 8080}}
	f := &fakeStore{services: map[string]api.Service{"api": svc}, tasks: []api.Task{
		{Metadata: api.Metadata{ID: "ready"}, Spec: api.TaskSpec{Labels: map[string]string{"app": "api"}}, Status: api.TaskStatus{Ready: true, Address: "10.64.0.2", NodeID: "n1", AssignmentGeneration: 2}},
		{Metadata: api.Metadata{ID: "unready"}, Spec: api.TaskSpec{Labels: map[string]string{"app": "api"}}, Status: api.TaskStatus{Address: "10.64.0.3"}},
		{Metadata: api.Metadata{ID: "other"}, Spec: api.TaskSpec{Labels: map[string]string{"app": "web"}}, Status: api.TaskStatus{Ready: true, Address: "10.64.0.4"}},
	}}
	c, _ := New(f)
	if err := c.Reconcile(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	got := f.services["api"].Status.Endpoints
	if len(got) != 1 || got[0].TaskID != "ready" || got[0].Port != 8080 {
		t.Fatalf("endpoints=%+v", got)
	}
}
