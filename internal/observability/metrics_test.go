package observability

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct{}

func (fake) ListNodes(context.Context) ([]api.Node, error) {
	return []api.Node{{Status: api.NodeStatus{Phase: api.NodeReady}}}, nil
}
func (fake) ListTasks(context.Context) ([]api.Task, error) {
	return []api.Task{{Metadata: api.Metadata{ID: "t"}, Spec: api.TaskSpec{WorkloadID: "w"}, Status: api.TaskStatus{Ready: true, NodeID: "n"}}}, nil
}
func (fake) ListWorkloads(context.Context) ([]api.Workload, error) { return []api.Workload{{}}, nil }
func (fake) ListServices(context.Context) ([]api.Service, error)   { return []api.Service{{}}, nil }
func (fake) ListEvents(context.Context) ([]api.Event, error)       { return []api.Event{{}, {}}, nil }
func TestMetricsCarryIdentifiers(t *testing.T) {
	r := httptest.NewRecorder()
	NewMetricsHandler(fake{}).ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	body := r.Body.String()
	for _, want := range []string{"glider_nodes{phase=\"READY\"} 1", "task_id=\"t\"", "workload_id=\"w\"", "node_id=\"n\"", "glider_events 2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}
