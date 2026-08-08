//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/santinomarial/glider/internal/agent"
	"github.com/santinomarial/glider/internal/lease"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
)

func main() {
	var endpoints string
	var nodeID, clusterID, dataRoot, networkCIDR string
	var resync time.Duration
	var leaseTTL, selfFence time.Duration
	var insecure bool
	flag.StringVar(&endpoints, "etcd-endpoints", "127.0.0.1:2379", "comma-separated etcd endpoints")
	flag.StringVar(&nodeID, "node-id", "", "this node's stable ID (required)")
	flag.StringVar(&clusterID, "cluster-id", "default", "Glider cluster ID")
	flag.StringVar(&dataRoot, "data-dir", "/var/lib/glider", "durable node data root")
	flag.StringVar(&networkCIDR, "network-cidr", "10.64.0.0/24", "node-local container subnet")
	flag.DurationVar(&resync, "resync", 30*time.Second, "full reconciliation interval")
	flag.DurationVar(&leaseTTL, "lease-ttl", 10*time.Second, "node lease TTL")
	flag.DurationVar(&selfFence, "self-fence-after", 25*time.Second, "maximum unproven lease duration before stopping workloads")
	flag.BoolVar(&insecure, "insecure-registry", false, "allow development registries over HTTP")
	flag.Parse()
	if nodeID == "" {
		fmt.Fprintln(os.Stderr, "gliderd: --node-id is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client, err := clientv3.New(clientv3.Config{Endpoints: split(endpoints), DialTimeout: 5 * time.Second})
	if err != nil {
		fatal(err)
	}
	defer client.Close()
	store, err := etcdstore.New(client, clusterID)
	if err != nil {
		fatal(err)
	}
	driver, err := agent.NewRuntimeDriver(dataRoot, networkCIDR, insecure)
	if err != nil {
		fatal(err)
	}
	reconciler, err := agent.New(filepath.Join(dataRoot, "agent", "assignments"), driver)
	if err != nil {
		fatal(err)
	}
	daemon, err := agent.NewDaemon(nodeID, store, reconciler, resync)
	if err != nil {
		fatal(err)
	}
	leaseManager, err := lease.New(client, clusterID, nodeID, uuid.NewString(), leaseTTL, selfFence)
	if err != nil { fatal(err) }
	runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()
	errs := make(chan error, 2)
	go func(){ errs <- daemon.Run(runCtx) }()
	go func(){ errs <- leaseManager.Run(ctx, func(context.Context) error { cancelRun(); fenceCtx,cancel:=context.WithTimeout(context.Background(),selfFence);defer cancel();return reconciler.Reconcile(fenceCtx,nil) }) }()
	err = <-errs; cancelRun()
	if err != nil && ctx.Err() == nil { fatal(err) }
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
func fatal(err error) { fmt.Fprintln(os.Stderr, "gliderd:", err); os.Exit(1) }
