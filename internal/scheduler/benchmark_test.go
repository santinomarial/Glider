package scheduler

import (
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	"testing"
)

func BenchmarkSchedule1000Nodes(b *testing.B) {
	nodes := make([]api.Node, 1000)
	for i := range nodes {
		nodes[i] = api.Node{Metadata: api.Metadata{ID: fmt.Sprintf("node-%04d", i)}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 8000, MemoryBytes: 16 << 30}}, Status: api.NodeStatus{Phase: api.NodeReady}}
	}
	task := api.Task{Spec: api.TaskSpec{WorkloadID: "bench", Resources: api.Resources{CPUMilli: 100, MemoryBytes: 64 << 20}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Schedule(task, nodes, nil); err != nil {
			b.Fatal(err)
		}
	}
}
