package main

import (
	"context"
	"net"
	"strings"
	"testing"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestCLIControlPlaneCallUsesTypedV2WireContract(t *testing.T) {
	service := new(cliControlServer)
	connection := startCLIControlServer(t, service)
	c := client{conn: connection, control: gliderv2.NewControlPlaneServiceClient(connection)}
	input := api.Task{
		Metadata: api.Metadata{ID: "task", IdempotencyKey: "request"},
		Spec: api.TaskSpec{
			Image:         "registry.example/app@sha256:abc",
			Labels:        map[string]string{"phase": "production"},
			RestartPolicy: api.RestartOnFailure,
		},
		Status: api.TaskStatus{Phase: api.TaskPending},
	}
	result, err := c.call(context.Background(), "PutTask", input)
	if err != nil {
		t.Fatal(err)
	}
	if service.putTask == nil || service.putTask.GetTask().GetSpec().GetRestartPolicy() != gliderv2.RestartPolicy_RESTART_POLICY_ON_FAILURE || service.putTask.GetTask().GetSpec().GetLabels()["phase"] != "production" {
		t.Fatalf("typed request = %+v", service.putTask)
	}
	metadata, _ := result["metadata"].(map[string]any)
	if metadata["revision"] != float64(41) {
		t.Fatalf("legacy-shaped CLI result = %+v", result)
	}
}

func TestCLIControlPlaneDispatchCoversEveryCommandMethod(t *testing.T) {
	connection := startCLIControlServer(t, new(cliControlServer))
	c := client{conn: connection, control: gliderv2.NewControlPlaneServiceClient(connection)}
	cases := []struct {
		method string
		input  any
	}{
		{"GetTask", map[string]any{"id": "task"}},
		{"DeleteTask", map[string]any{"id": "task", "revision": 1}},
		{"PutWorkload", api.Workload{Metadata: api.Metadata{ID: "workload"}, Spec: api.WorkloadSpec{Template: api.TaskSpec{Image: "image"}}}},
		{"DeleteWorkload", map[string]any{"id": "workload", "revision": 1}},
		{"ListWorkloads", map[string]any{}},
		{"DeleteService", map[string]any{"id": "service", "revision": 1}},
		{"ListServices", map[string]any{}},
		{"ListNodes", map[string]any{}},
		{"DrainNode", map[string]any{"id": "node", "revision": 1}},
		{"RemoveNode", map[string]any{"id": "node", "revision": 1}},
		{"ListTasks", map[string]any{}},
		{"ListEvents", map[string]any{}},
		{"PutSecret", api.Secret{Metadata: api.Metadata{ID: "secret"}, Data: map[string][]byte{"key": []byte("value")}}},
		{"ListSecrets", map[string]any{}},
		{"DeleteSecret", map[string]any{"id": "secret", "revision": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			_, err := c.call(context.Background(), tc.method, tc.input)
			if status.Code(err) != codes.Unimplemented || strings.Contains(err.Error(), "unsupported control-plane method") {
				t.Fatalf("dispatch error = %v", err)
			}
		})
	}
}

type cliControlServer struct {
	gliderv2.UnimplementedControlPlaneServiceServer
	putTask *gliderv2.PutTaskRequest
}

func (s *cliControlServer) PutTask(_ context.Context, request *gliderv2.PutTaskRequest) (*gliderv2.PutTaskResponse, error) {
	s.putTask = request
	request.Task.Metadata.Revision = 41
	return &gliderv2.PutTaskResponse{Task: request.Task}, nil
}

func startCLIControlServer(t *testing.T, service gliderv2.ControlPlaneServiceServer) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	gliderv2.RegisterControlPlaneServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///cli-control-plane",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}
