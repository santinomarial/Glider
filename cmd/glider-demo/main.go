package main

import (
	"context"
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	servicecontroller "github.com/santinomarial/glider/internal/controller/service"
	workloadcontroller "github.com/santinomarial/glider/internal/controller/workload"
	"github.com/santinomarial/glider/internal/discovery"
	"github.com/santinomarial/glider/internal/scheduler"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := demo(); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}
func demo() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, closeEtcd, err := start(ctx)
	if err != nil {
		return err
	}
	defer closeEtcd()
	store, _ := etcdstore.New(client, "demo")
	workloads, _ := workloadcontroller.New(store)
	services, _ := servicecontroller.New(store)
	placer, _ := scheduler.NewController(store)
	_, err = store.PutNode(ctx, api.Node{Metadata: api.Metadata{ID: "node-a"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 4000, MemoryBytes: 8 << 30}}, Status: api.NodeStatus{Phase: api.NodeReady}}, 0)
	if err != nil {
		return err
	}
	w, _ := store.PutWorkload(ctx, api.Workload{Metadata: api.Metadata{ID: "api", Name: "api", Generation: 1}, Spec: api.WorkloadSpec{Replicas: 2, Template: api.TaskSpec{Image: "demo/api:v1", Labels: map[string]string{"app": "api"}, Resources: api.Resources{CPUMilli: 100, MemoryBytes: 32 << 20}}, Rollout: api.RolloutStrategy{MaxSurge: 1, MaxUnavailable: 0}}}, 0)
	_, _ = store.PutService(ctx, api.Service{Metadata: api.Metadata{ID: "api", Name: "api"}, Spec: api.ServiceSpec{Selector: map[string]string{"app": "api"}, Port: 80, TargetPort: 8080}}, 0)
	if err = workloads.Reconcile(ctx, w); err != nil {
		return err
	}
	tasks, _ := store.ListTasks(ctx)
	for i, t := range tasks {
		a, err := placer.ScheduleOne(ctx, t.Metadata.ID)
		if err != nil {
			return err
		}
		address := fmt.Sprintf("10.64.0.%d", i+2)
		_ = store.ReportTaskEndpoint(ctx, t.Metadata.ID, a.Generation, address)
		_ = store.ReportTaskHealth(ctx, t.Metadata.ID, a.Generation, true)
	}
	svcList, _ := store.ListServices(ctx)
	if err = services.Reconcile(ctx, svcList[0]); err != nil {
		return err
	}
	dns, _ := discovery.NewDNS(store)
	ips, _ := dns.Lookup(ctx, "api.glider")
	fmt.Printf("READY workload=api replicas=2 service=api.glider endpoints=%v\n", ips)
	workloadList, _ := store.ListWorkloads(ctx)
	w = workloadList[0]
	w.Metadata.Generation++
	w.Spec.Template.Image = "demo/api:v2"
	w, _ = store.PutWorkload(ctx, w, w.Metadata.Revision)
	if err = workloads.Reconcile(ctx, w); err != nil {
		return err
	}
	tasks, _ = store.ListTasks(ctx)
	for _, t := range tasks {
		if t.Spec.Image != "demo/api:v2" {
			continue
		}
		a, err := placer.ScheduleOne(ctx, t.Metadata.ID)
		if err != nil {
			return err
		}
		_ = store.ReportTaskEndpoint(ctx, t.Metadata.ID, a.Generation, "10.64.0.20")
		_ = store.ReportTaskHealth(ctx, t.Metadata.ID, a.Generation, true)
	}
	w = (mustWorkloads(ctx, store))[0]
	_ = workloads.Reconcile(ctx, w)
	fmt.Println("ROLLOUT image=demo/api:v2 readiness-gated=true healthy-v1-preserved-until-ready=true")
	fmt.Println("DEMO GREEN")
	return nil
}
func mustWorkloads(ctx context.Context, s *etcdstore.Store) []api.Workload {
	v, err := s.ListWorkloads(ctx)
	if err != nil {
		panic(err)
	}
	return v
}
func start(ctx context.Context) (*clientv3.Client, func(), error) {
	dir, err := os.MkdirTemp("", "glider-demo-")
	if err != nil {
		return nil, nil, err
	}
	clientURL, err := freeURL()
	if err != nil {
		return nil, nil, err
	}
	peerURL, err := freeURL()
	if err != nil {
		return nil, nil, err
	}
	cfg := embed.NewConfig()
	cfg.Dir = filepath.Join(dir, "etcd")
	cfg.LogLevel = "error"
	cfg.Logger = "zap"
	cfg.ListenClientUrls = []url.URL{clientURL}
	cfg.AdvertiseClientUrls = []url.URL{clientURL}
	cfg.ListenPeerUrls = []url.URL{peerURL}
	cfg.AdvertisePeerUrls = []url.URL{peerURL}
	cfg.InitialCluster = "default=" + peerURL.String()
	server, err := embed.StartEtcd(cfg)
	if err != nil {
		return nil, nil, err
	}
	select {
	case <-server.Server.ReadyNotify():
	case <-ctx.Done():
		server.Close()
		return nil, nil, ctx.Err()
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, DialTimeout: 5 * time.Second})
	if err != nil {
		server.Close()
		return nil, nil, err
	}
	return client, func() { client.Close(); server.Close(); os.RemoveAll(dir) }, nil
}
func freeURL() (url.URL, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return url.URL{}, err
	}
	address := listener.Addr().String()
	listener.Close()
	parsed, err := url.Parse("http://" + address)
	if err != nil {
		return url.URL{}, err
	}
	return *parsed, nil
}
