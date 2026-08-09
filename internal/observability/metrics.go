package observability

import (
	"context"
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	"net/http"
	"sort"
)

type Snapshotter interface {
	ListNodes(context.Context) ([]api.Node, error)
	ListTasks(context.Context) ([]api.Task, error)
	ListWorkloads(context.Context) ([]api.Workload, error)
	ListServices(context.Context) ([]api.Service, error)
	ListEvents(context.Context) ([]api.Event, error)
}
type Handler struct{ store Snapshotter }

func NewMetricsHandler(store Snapshotter) http.Handler { return &Handler{store: store} }
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	nodes, e1 := h.store.ListNodes(r.Context())
	tasks, e2 := h.store.ListTasks(r.Context())
	workloads, e3 := h.store.ListWorkloads(r.Context())
	services, e4 := h.store.ListServices(r.Context())
	events, e5 := h.store.ListEvents(r.Context())
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		http.Error(w, "metrics snapshot unavailable", http.StatusServiceUnavailable)
		return
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Metadata.ID < tasks[j].Metadata.ID })
	fmt.Fprintln(w, "# HELP glider_nodes Number of nodes by phase.\n# TYPE glider_nodes gauge")
	counts := map[api.NodePhase]int{}
	for _, n := range nodes {
		counts[n.Status.Phase]++
	}
	for _, p := range []api.NodePhase{api.NodeReady, api.NodeSuspect, api.NodeUnreachable, api.NodeDraining} {
		fmt.Fprintf(w, "glider_nodes{phase=%q} %d\n", p, counts[p])
	}
	fmt.Fprintln(w, "# HELP glider_task_ready Whether a task is Ready.\n# TYPE glider_task_ready gauge")
	for _, t := range tasks {
		ready := 0
		if t.Status.Ready {
			ready = 1
		}
		fmt.Fprintf(w, "glider_task_ready{task_id=%q,workload_id=%q,node_id=%q} %d\n", t.Metadata.ID, t.Spec.WorkloadID, t.Status.NodeID, ready)
	}
	fmt.Fprintf(w, "glider_workloads %d\nglider_services %d\nglider_events %d\n", len(workloads), len(services), len(events))
}
