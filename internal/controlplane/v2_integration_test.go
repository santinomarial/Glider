package controlplane

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
	storeapi "github.com/santinomarial/glider/internal/store"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestV2TaskSchedulingUsesAuthoritativeLegacyPath(t *testing.T) {
	client := startControlPlaneEtcd(t)
	store, err := etcdstore.New(client, "typed-api")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	RegisterV2(server, legacy)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	service := gliderv2.NewControlPlaneServiceClient(connection)
	ctx := context.Background()
	node, err := store.PutNode(ctx, api.Node{Metadata: api.Metadata{ID: "node"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000, MemoryBytes: 1 << 20}}, Status: api.NodeStatus{Phase: api.NodeReady}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = node

	created, err := service.PutTask(ctx, &gliderv2.PutTaskRequest{Task: &gliderv2.Task{
		Metadata: &gliderv2.Metadata{Id: "task", IdempotencyKey: "create-task"},
		Spec:     &gliderv2.TaskSpec{Image: "registry.example/app:1", Resources: &gliderv2.Resources{CpuMilli: 100, MemoryBytes: 1024}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetTask().GetStatus().GetPhase() != gliderv2.TaskPhase_TASK_PHASE_PENDING || created.GetTask().GetMetadata().GetRevision() == 0 {
		t.Fatalf("created task=%+v", created.GetTask())
	}
	assignment, err := service.Schedule(ctx, &gliderv2.ScheduleRequest{TaskId: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if assignment.GetAssignment().GetNodeId() != "node" || assignment.GetAssignment().GetGeneration() != 1 {
		t.Fatalf("assignment=%+v", assignment.GetAssignment())
	}
	tasks, err := service.ListTasks(ctx, &gliderv2.ListTasksRequest{})
	if err != nil || len(tasks.GetItems()) != 1 || tasks.GetItems()[0].GetStatus().GetPhase() != gliderv2.TaskPhase_TASK_PHASE_SCHEDULED {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}

	_, err = service.PutTask(ctx, &gliderv2.PutTaskRequest{Task: &gliderv2.Task{
		Metadata: &gliderv2.Metadata{Id: "forged", IdempotencyKey: "forged"},
		Spec:     &gliderv2.TaskSpec{Image: "registry.example/app:1"},
		Status:   &gliderv2.TaskStatus{Phase: gliderv2.TaskPhase_TASK_PHASE_RUNNING, NodeId: "node", Ready: true},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("forged status error=%v", err)
	}
	if _, err := store.GetTask(ctx, "forged"); !errors.Is(err, storeapi.ErrNotFound) {
		t.Fatalf("forged task persisted: %v", err)
	}
}

func startControlPlaneEtcd(t *testing.T) *clientv3.Client {
	t.Helper()
	config := embed.NewConfig()
	config.Dir = t.TempDir()
	config.LogLevel = "error"
	config.Logger = "zap"
	clientURL := controlPlaneFreeURL(t)
	peerURL := controlPlaneFreeURL(t)
	config.ListenClientUrls = []url.URL{clientURL}
	config.AdvertiseClientUrls = []url.URL{clientURL}
	config.ListenPeerUrls = []url.URL{peerURL}
	config.AdvertisePeerUrls = []url.URL{peerURL}
	config.InitialCluster = "default=" + peerURL.String()
	server, err := embed.StartEtcd(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("embedded etcd did not become ready")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func controlPlaneFreeURL(t *testing.T) url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	parsed, err := url.Parse("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	return *parsed
}
