package nodeops

import (
	"context"
	"encoding/base64"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"google.golang.org/protobuf/types/known/structpb"
	"testing"
)

type fakeStore struct{ values []api.Assignment }

func (f fakeStore) ListAssignments(context.Context) ([]api.Assignment, error) { return f.values, nil }

type fakeRuntime struct{}

func (fakeRuntime) Logs(api.Assignment, int64) ([]byte, error) { return []byte("hello"), nil }
func (fakeRuntime) Stats(api.Assignment) (cgroup.Stats, error) {
	return cgroup.Stats{Memory: cgroup.MemoryStats{CurrentBytes: 42}}, nil
}
func request(task string, generation float64) *structpb.Struct {
	v, _ := structpb.NewStruct(map[string]any{"task_id": task, "generation": generation})
	return v
}
func TestOperationsFenceStaleGeneration(t *testing.T) {
	service, _ := New("n", fakeStore{[]api.Assignment{{TaskID: "t", NodeID: "n", Generation: 2}}}, fakeRuntime{})
	if _, err := service.GetLogs(context.Background(), request("t", 1)); err == nil {
		t.Fatal("stale generation accepted")
	}
	result, err := service.GetLogs(context.Background(), request("t", 2))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(result.AsMap()["data_base64"].(string))
	if string(data) != "hello" {
		t.Fatalf("data=%q", data)
	}
}
