package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"

	"github.com/santinomarial/glider/internal/api"
	servicecontroller "github.com/santinomarial/glider/internal/controller/service"
	workloadcontroller "github.com/santinomarial/glider/internal/controller/workload"
	"github.com/santinomarial/glider/internal/controlplane"
	"github.com/santinomarial/glider/internal/discovery"
	"github.com/santinomarial/glider/internal/lease"
	"github.com/santinomarial/glider/internal/scheduler"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "gRPC listen address")
	endpoints := flag.String("etcd-endpoints", "127.0.0.1:2379", "comma-separated etcd endpoints")
	clusterID := flag.String("cluster-id", "default", "Glider cluster ID")
	dnsListen := flag.String("dns-listen", "", "optional authoritative cluster DNS UDP address (for example :53)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client, err := clientv3.New(clientv3.Config{Endpoints: split(*endpoints), DialTimeout: 5 * time.Second})
	if err != nil {
		fatal(err)
	}
	defer client.Close()
	store, err := etcdstore.New(client, *clusterID)
	if err != nil {
		fatal(err)
	}
	service, err := controlplane.New(store)
	if err != nil {
		fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fatal(err)
	}
	server := grpc.NewServer()
	controlplane.Register(server, service)
	workloads, err := workloadcontroller.New(store)
	if err != nil {
		fatal(err)
	}
	services, err := servicecontroller.New(store)
	if err != nil {
		fatal(err)
	}
	schedulerController, err := scheduler.NewController(store)
	if err != nil {
		fatal(err)
	}
	go func() { _ = workloads.Run(ctx, 2*time.Second) }()
	go func() { _ = services.Run(ctx, 2*time.Second) }()
	if *dnsListen != "" {
		dns, err := discovery.NewDNS(store)
		if err != nil {
			fatal(err)
		}
		go func() {
			if err := dns.ServeUDP(ctx, *dnsListen); err != nil && ctx.Err() == nil {
				fatal(err)
			}
		}()
		fmt.Fprintf(os.Stderr, "glider-controlplane: cluster DNS listening on %s/udp\n", *dnsListen)
	}
	go schedulePending(ctx, store, schedulerController)
	go func() { _ = lease.NewMonitor(client, *clusterID, store, 20*time.Second, 2*time.Second).Run(ctx) }()
	go func() { <-ctx.Done(); server.GracefulStop() }()
	fmt.Fprintf(os.Stderr, "glider-controlplane: listening on %s\n", listener.Addr())
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}
func split(value string) []string {
	var out []string
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "glider-controlplane:", err); os.Exit(1) }

type taskLister interface {
	ListTasks(context.Context) ([]api.Task, error)
}

func schedulePending(ctx context.Context, store taskLister, controller *scheduler.Controller) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		tasks, err := store.ListTasks(ctx)
		if err == nil {
			for _, task := range tasks {
				if task.Status.Phase == api.TaskPending {
					_, _ = controller.ScheduleOne(ctx, task.Metadata.ID)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
