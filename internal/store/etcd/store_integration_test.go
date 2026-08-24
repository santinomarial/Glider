package etcd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"

	"github.com/santinomarial/glider/internal/api"
	secretapi "github.com/santinomarial/glider/internal/secret"
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

func TestConcurrentQuotaAdmissionHasOneWinnerAndDeleteReleasesUsage(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "quota-cluster")
	ctx := context.Background()
	limits := QuotaLimits{Tasks: 1, Workloads: 2, Services: 2, Resources: api.Resources{CPUMilli: 500, MemoryBytes: 1024}}
	if err := s.ConfigureQuota(ctx, limits); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			<-start
			_, err := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: id}, Spec: api.TaskSpec{Image: "image", Resources: api.Resources{CPUMilli: 500, MemoryBytes: 1024}}}, 0)
			errs <- err
		}(id)
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-errs
		if err == nil {
			winners++
		} else if !errors.Is(err, storeapi.ErrConflict) && !errors.Is(err, storeapi.ErrQuotaExceeded) {
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("quota winners = %d", winners)
	}
	tasks, _ := s.ListTasks(ctx)
	if len(tasks) != 1 {
		t.Fatalf("stored tasks = %d", len(tasks))
	}
	if err := s.DeleteTask(ctx, tasks[0].Metadata.ID, tasks[0].Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "replacement"}, Spec: api.TaskSpec{Image: "image", Resources: api.Resources{CPUMilli: 500, MemoryBytes: 1024}}}, 0); err != nil {
		t.Fatalf("quota was not released: %v", err)
	}
}

func TestQuotaConfigurationMustMatchAcrossReplicas(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "quota-config-cluster")
	ctx := context.Background()
	limits := QuotaLimits{Tasks: 1, Workloads: 1, Services: 1, Resources: api.Resources{CPUMilli: 1, MemoryBytes: 1}}
	if err := s.ConfigureQuota(ctx, limits); err != nil {
		t.Fatal(err)
	}
	limits.Tasks = 2
	if err := s.ConfigureQuota(ctx, limits); err == nil {
		t.Fatal("mismatched replica quota was accepted")
	}
}

func TestSecretPersistenceContainsOnlyAuthenticatedCiphertext(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "secret-cluster")
	cipher, _ := secretapi.NewCipher(bytes.Repeat([]byte{3}, 32), "secret-cluster")
	plaintext := []byte("never-store-this-plaintext")
	envelope, err := cipher.Encrypt(api.Secret{Metadata: api.Metadata{ID: "database"}, Data: map[string][]byte{"password": plaintext}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.PutSecret(context.Background(), envelope, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := client.Get(context.Background(), s.key("secrets", "database"))
	if err != nil || len(raw.Kvs) != 1 {
		t.Fatalf("raw secret = %v, %v", raw.Kvs, err)
	}
	if bytes.Contains(raw.Kvs[0].Value, plaintext) {
		t.Fatal("etcd value contains secret plaintext")
	}
	stored, err := s.GetSecret(context.Background(), "database")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := cipher.Decrypt(stored)
	if err != nil || !bytes.Equal(decoded.Data["password"], plaintext) {
		t.Fatalf("decoded secret = %#v, %v", decoded, err)
	}
	if err := s.DeleteSecret(context.Background(), "database", saved.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestEventRetentionEnforcesAgeAndCountBounds(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "event-retention-cluster")
	ctx := context.Background()
	now := time.Now().UTC()
	for index, age := range []time.Duration{10 * time.Hour, 9 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour} {
		id := fmt.Sprintf("event-%d", index)
		if _, err := s.PutEvent(ctx, api.Event{Metadata: api.Metadata{ID: id}, Time: now.Add(-age), Type: "Normal", Reason: "Test", ObjectKind: "Task", ObjectID: "task"}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.PruneEvents(ctx, now.Add(-8*time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d", removed)
	}
	events, err := s.ListEvents(ctx)
	if err != nil || len(events) != 2 || events[0].Metadata.ID != "event-3" || events[1].Metadata.ID != "event-4" {
		t.Fatalf("retained events = %+v, %v", events, err)
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

func TestDeleteServiceRequiresCurrentRevision(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "service-delete-cluster")
	ctx := context.Background()
	service, err := s.PutService(ctx, api.Service{Metadata: api.Metadata{ID: "web"}, Spec: api.ServiceSpec{Selector: map[string]string{"app": "web"}, Port: 80, TargetPort: 8080}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	updated := service
	updated.Spec.TargetPort = 9090
	updated, err = s.PutService(ctx, updated, service.Metadata.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteService(ctx, "web", service.Metadata.Revision); !errors.Is(err, storeapi.ErrConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := s.DeleteService(ctx, "web", updated.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	services, err := s.ListServices(ctx)
	if err != nil || len(services) != 0 {
		t.Fatalf("services = %v, %v", services, err)
	}
}

func TestEvictUnreachableNodeRequeuesWithNewGeneration(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "evict-cluster")
	ctx := context.Background()
	task, _ := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: 400}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	a, _ := s.PutNode(ctx, readyNode("a"), 0)
	_, _ = s.PutNode(ctx, readyNode("b"), 0)
	first, err := s.Bind(ctx, storeapi.BindRequest{TaskID: "task", TaskRevision: task.Metadata.Revision, NodeID: "a", NodeRevision: a.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvictNodeAssignments(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.GetTask(ctx, "task")
	if pending.Status.Phase != api.TaskPending || pending.Metadata.Generation != first.Generation {
		t.Fatalf("pending=%+v", pending)
	}
	nodes, _ := s.ListNodes(ctx)
	var nodeB api.Node
	for _, node := range nodes {
		if node.Metadata.ID == "b" {
			nodeB = node
		}
	}
	second, err := s.Bind(ctx, storeapi.BindRequest{TaskID: "task", TaskRevision: pending.Metadata.Revision, NodeID: "b", NodeRevision: nodeB.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not advance: %d -> %d", first.Generation, second.Generation)
	}
}

func TestRemoveNodeRequiresDrainAndAbsentLease(t *testing.T) {
	client := startEtcd(t)
	store, _ := New(client, "remove-node-cluster")
	ctx := context.Background()
	node, err := store.PutNode(ctx, readyNode("node-a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveNode(ctx, "node-a", node.Metadata.Revision); !errors.Is(err, storeapi.ErrNodeActive) {
		t.Fatalf("ready node removal error = %v", err)
	}
	node.Spec.Unschedulable = true
	node.Status.Phase = api.NodeDraining
	node, err = store.PutNode(ctx, node, node.Metadata.Revision)
	if err != nil {
		t.Fatal(err)
	}
	leaseKey := store.key("leases/nodes", "node-a")
	if _, err = client.Put(ctx, leaseKey, "live-owner"); err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveNode(ctx, "node-a", node.Metadata.Revision); !errors.Is(err, storeapi.ErrNodeActive) {
		t.Fatalf("live node removal error = %v", err)
	}
	if _, err = client.Delete(ctx, leaseKey); err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveNode(ctx, "node-a", node.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetNode(ctx, "node-a"); !errors.Is(err, storeapi.ErrNotFound) {
		t.Fatalf("removed node still exists: %v", err)
	}
}

func TestTaskLifecycleReportsGenerationAndCompletesAtomically(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "lifecycle-cluster")
	ctx := context.Background()
	task, _ := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: 400}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	node, _ := s.PutNode(ctx, readyNode("node"), 0)
	a, err := s.Bind(ctx, storeapi.BindRequest{TaskID: task.Metadata.ID, TaskRevision: task.Metadata.Revision, NodeID: node.Metadata.ID, NodeRevision: node.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReportTaskRunning(ctx, task.Metadata.ID, a.Generation); err != nil {
		t.Fatal(err)
	}
	running, _ := s.GetTask(ctx, task.Metadata.ID)
	if running.Status.Phase != api.TaskRunning || running.Status.StartedAt.IsZero() {
		t.Fatalf("running=%+v", running)
	}
	if err := s.ReportTaskRunning(ctx, task.Metadata.ID, a.Generation-1); !errors.Is(err, storeapi.ErrConflict) {
		t.Fatalf("stale running report: %v", err)
	}
	code := 23
	if err := s.CompleteTask(ctx, task.Metadata.ID, a.Generation, &code, "workload failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, task.Metadata.ID, a.Generation, &code, "workload failed"); err != nil {
		t.Fatalf("completion retry: %v", err)
	}
	completed, _ := s.GetTask(ctx, task.Metadata.ID)
	if completed.Status.Phase != api.TaskTerminated || completed.Status.ExitCode == nil || *completed.Status.ExitCode != code || completed.Status.FinishedAt.IsZero() {
		t.Fatalf("completed=%+v", completed)
	}
	nodes, _ := s.ListNodes(ctx)
	if nodes[0].Status.Reserved.CPUMilli != 0 {
		t.Fatalf("reservation=%d", nodes[0].Status.Reserved.CPUMilli)
	}
	assignments, _ := s.ListAssignments(ctx)
	if len(assignments) != 0 {
		t.Fatalf("assignments=%d", len(assignments))
	}
}

func TestRestartBackoffPersistsAndBlocksBinding(t *testing.T) {
	client := startEtcd(t)
	s, _ := New(client, "restart-backoff-cluster")
	ctx := context.Background()
	task, _ := s.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Resources: api.Resources{CPUMilli: 400}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	node, _ := s.PutNode(ctx, readyNode("node"), 0)
	a, err := s.Bind(ctx, storeapi.BindRequest{TaskID: task.Metadata.ID, TaskRevision: task.Metadata.Revision, NodeID: node.Metadata.ID, NodeRevision: node.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	if err := s.RestartTask(ctx, task.Metadata.ID, a.Generation); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.GetTask(ctx, task.Metadata.ID)
	if pending.Status.Phase != api.TaskPending || pending.Status.RestartCount != 1 || !pending.Status.RestartNotBefore.After(before) {
		t.Fatalf("pending restart=%+v", pending.Status)
	}
	nodes, _ := s.ListNodes(ctx)
	request := storeapi.BindRequest{TaskID: task.Metadata.ID, TaskRevision: pending.Metadata.Revision, NodeID: nodes[0].Metadata.ID, NodeRevision: nodes[0].Metadata.Revision}
	if _, err := s.Bind(ctx, request); !errors.Is(err, storeapi.ErrRestartBackoff) {
		t.Fatalf("binding during backoff: %v", err)
	}
	pending.Status.RestartNotBefore = time.Now().UTC().Add(-time.Second)
	pending, err = s.PutTask(ctx, pending, pending.Metadata.Revision)
	if err != nil {
		t.Fatal(err)
	}
	request.TaskRevision = pending.Metadata.Revision
	second, err := s.Bind(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= a.Generation {
		t.Fatalf("generation did not advance: %d -> %d", a.Generation, second.Generation)
	}
}

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
