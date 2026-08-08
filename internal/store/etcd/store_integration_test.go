package etcd

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"github.com/santinomarial/glider/internal/api"
	storeapi "github.com/santinomarial/glider/internal/store"
)

func TestEtcdConcurrentBindHasOneWinner(t *testing.T) {
	client := startEtcd(t)
	s, err := New(client, "test-cluster")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{WorkloadID: "work", Resources: api.Resources{CPUMilli: 500}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.PutNode(ctx, readyNode("a"), 0)
	b, _ := s.PutNode(ctx, readyNode("b"), 0)
	reqs := []storeapi.BindRequest{{TaskID: "task", TaskRevision: task.Metadata.Revision, NodeID: "a", NodeRevision: a.Metadata.Revision}, {TaskID: "task", TaskRevision: task.Metadata.Revision, NodeID: "b", NodeRevision: b.Metadata.Revision}}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, r := range reqs {
		wg.Add(1)
		go func(r storeapi.BindRequest) { defer wg.Done(); _, err := s.Bind(ctx, r); errs <- err }(r)
	}
	wg.Wait()
	close(errs)
	wins, losses := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, storeapi.ErrConflict) || errors.Is(err, storeapi.ErrAlreadyAssigned) {
			losses++
		} else {
			t.Errorf("unexpected %v", err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("wins=%d losses=%d", wins, losses)
	}
	assignments, _ := s.ListAssignments(ctx)
	if len(assignments) != 1 {
		t.Fatalf("assignments=%d", len(assignments))
	}
	stored, _ := s.GetTask(ctx, "task")
	if stored.Status.Phase != api.TaskScheduled || stored.Metadata.Generation != 1 {
		t.Fatalf("task=%+v", stored)
	}
}

func TestDeleteAssignedTaskAtomicallyReleasesReservation(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "delete-cluster")
	ctx := context.Background()
	task, _ := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: 400}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	node, _ := s.PutNode(ctx, readyNode("node"), 0)
	_, err := s.Bind(ctx, storeapi.BindRequest{TaskID: "task", TaskRevision: task.Metadata.Revision, NodeID: "node", NodeRevision: node.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := s.GetTask(ctx, "task")
	if err := s.DeleteTask(ctx, "task", stored.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.ListNodes(ctx)
	if nodes[0].Status.Reserved.CPUMilli != 0 {
		t.Fatalf("reservation=%d", nodes[0].Status.Reserved.CPUMilli)
	}
	if assignments, _ := s.ListAssignments(ctx); len(assignments) != 0 {
		t.Fatalf("assignments=%d", len(assignments))
	}
}

func TestEvictUnreachableNodeRequeuesWithNewGeneration(t *testing.T){client:=startEtcd(t);s,_:=New(client,"evict-cluster");ctx:=context.Background();task,_:=s.PutTask(ctx,api.Task{Metadata:api.Metadata{ID:"task"},Spec:api.TaskSpec{Resources:api.Resources{CPUMilli:400}},Status:api.TaskStatus{Phase:api.TaskPending}},0);a,_:=s.PutNode(ctx,readyNode("a"),0);b,_:=s.PutNode(ctx,readyNode("b"),0);first,err:=s.Bind(ctx,storeapi.BindRequest{TaskID:"task",TaskRevision:task.Metadata.Revision,NodeID:"a",NodeRevision:a.Metadata.Revision});if err!=nil{t.Fatal(err)};if err:=s.EvictNodeAssignments(ctx,"a");err!=nil{t.Fatal(err)};pending,_:=s.GetTask(ctx,"task");if pending.Status.Phase!=api.TaskPending||pending.Metadata.Generation!=first.Generation{t.Fatalf("pending=%+v",pending)};nodes,_:=s.ListNodes(ctx);var nodeB api.Node;for _,node:=range nodes{if node.Metadata.ID=="b"{nodeB=node}};second,err:=s.Bind(ctx,storeapi.BindRequest{TaskID:"task",TaskRevision:pending.Metadata.Revision,NodeID:"b",NodeRevision:nodeB.Metadata.Revision});if err!=nil{t.Fatal(err)};if second.Generation<=first.Generation{t.Fatalf("generation did not advance: %d -> %d",first.Generation,second.Generation)}}

func readyNode(id string) api.Node {
	return api.Node{Metadata: api.Metadata{ID: id}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000}}, Status: api.NodeStatus{Phase: api.NodeReady}}
}
func startEtcd(t *testing.T) *clientv3.Client {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	cfg.Logger = "zap"
	clientURL := freeURL(t)
	peerURL := freeURL(t)
	cfg.ListenClientUrls = []url.URL{clientURL}
	cfg.AdvertiseClientUrls = []url.URL{clientURL}
	cfg.ListenPeerUrls = []url.URL{peerURL}
	cfg.AdvertisePeerUrls = []url.URL{peerURL}
	cfg.InitialCluster = "default=" + peerURL.String()
	server, err := embed.StartEtcd(cfg)
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
	t.Cleanup(func() { client.Close() })
	return client
}
func freeURL(t *testing.T) url.URL {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	u, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}
