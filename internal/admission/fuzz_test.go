package admission

import (
	"encoding/json"
	"testing"

	"github.com/santinomarial/glider/internal/api"
)

// FuzzAdmissionJSON exercises every externally mutable resource decoder and
// validator with the same one-MiB ceiling enforced by the gRPC server.
func FuzzAdmissionJSON(f *testing.F) {
	f.Add([]byte(`{"metadata":{"id":"task","idempotency_key":"seed"},"spec":{"image":"example/image"}}`))
	f.Add([]byte(`{"metadata":{"id":"node"},"spec":{"capacity":{"cpu_milli":1000,"memory_bytes":1024}}}`))
	f.Add([]byte(`{"metadata":{"id":"service","idempotency_key":"seed"},"spec":{"selector":{"app":"web"},"port":80,"target_port":8080}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var task api.Task
		if json.Unmarshal(data, &task) == nil {
			_ = Task(task)
		}
		var workload api.Workload
		if json.Unmarshal(data, &workload) == nil {
			_ = Workload(workload)
		}
		var node api.Node
		if json.Unmarshal(data, &node) == nil {
			_ = Node(node)
		}
		var service api.Service
		if json.Unmarshal(data, &service) == nil {
			_ = Service(service)
		}
		var event api.Event
		if json.Unmarshal(data, &event) == nil {
			_ = Event(event)
		}
		var secret api.Secret
		if json.Unmarshal(data, &secret) == nil {
			_ = Secret(secret)
		}
	})
}
