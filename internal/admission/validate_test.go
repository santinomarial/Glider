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

func TestNetworkPolicyValidation(t *testing.T) {
	valid := api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Image: "app", NetworkPolicy: api.NetworkPolicy{DefaultDenyEgress: true, Egress: []api.NetworkRule{{CIDR: "10.64.0.0/16", Protocol: "tcp", Ports: []uint16{443}}}}}}
	if err := Task(valid); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []api.NetworkPolicy{
		{Egress: []api.NetworkRule{{CIDR: "10.0.0.0/8"}}},
		{DefaultDenyEgress: true, Egress: []api.NetworkRule{{CIDR: "10.0.0.1/8"}}},
		{DefaultDenyEgress: true, Egress: []api.NetworkRule{{CIDR: "0.0.0.0/0", Protocol: "icmp", Ports: []uint16{80}}}},
		{DefaultDenyIngress: true, Ingress: []api.NetworkRule{{CIDR: "0.0.0.0/0", Protocol: "sctp"}}},
	} {
		invalid := valid
		invalid.Spec.NetworkPolicy = policy
		if err := Task(invalid); err == nil {
			t.Fatalf("accepted network policy %+v", policy)
		}
	}
}
