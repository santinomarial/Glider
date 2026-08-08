package admission

import (
	"github.com/santinomarial/glider/internal/api"
	"strings"
	"testing"
)

func TestTaskRejectsAbusiveInputs(t *testing.T) {
	base := api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Image: "app"}}
	cases := []api.Task{base, {Metadata: api.Metadata{ID: "../escape"}, Spec: base.Spec}, {Metadata: base.Metadata, Spec: api.TaskSpec{Image: ""}}, {Metadata: base.Metadata, Spec: api.TaskSpec{Image: "app", Command: []string{strings.Repeat("x", 129<<10)}}}, {Metadata: base.Metadata, Spec: api.TaskSpec{Image: "app", HostPorts: []uint16{80, 80}}}}
	if err := Task(cases[0]); err != nil {
		t.Fatal(err)
	}
	for i, value := range cases[1:] {
		if err := Task(value); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
func TestWorkloadAndServiceBounds(t *testing.T) {
	w := api.Workload{Metadata: api.Metadata{ID: "app"}, Spec: api.WorkloadSpec{Replicas: 10001, Template: api.TaskSpec{Image: "app"}}}
	if Workload(w) == nil {
		t.Fatal("replica bomb accepted")
	}
	s := api.Service{Metadata: api.Metadata{ID: "api"}, Spec: api.ServiceSpec{Port: 80, TargetPort: 80}}
	if Service(s) == nil {
		t.Fatal("empty selector accepted")
	}
}
