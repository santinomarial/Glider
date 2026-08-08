//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"

	"github.com/santinomarial/glider/internal/agent"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/lease"
	"github.com/santinomarial/glider/internal/nodeops"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	"github.com/santinomarial/glider/internal/transport"
)

func main() {
	var endpoints string
	var nodeID, clusterID, dataRoot, networkCIDR string
	var resync time.Duration
	var leaseTTL, selfFence time.Duration
	var insecure bool
	var etcdTLSCert, etcdTLSKey, etcdCA, etcdServerName string
	var insecureEtcd bool
	var operationsListen, tlsCert, tlsKey, clientCA string
	var execHelper string
	flag.StringVar(&endpoints, "etcd-endpoints", "127.0.0.1:2379", "comma-separated etcd endpoints")
	flag.StringVar(&nodeID, "node-id", "", "this node's stable ID (required)")
	flag.StringVar(&clusterID, "cluster-id", "default", "Glider cluster ID")
	flag.StringVar(&dataRoot, "data-dir", "/var/lib/glider", "durable node data root")
	flag.StringVar(&networkCIDR, "network-cidr", "10.64.0.0/24", "node-local container subnet")
	flag.DurationVar(&resync, "resync", 30*time.Second, "full reconciliation interval")
	flag.DurationVar(&leaseTTL, "lease-ttl", 10*time.Second, "node lease TTL")
	flag.DurationVar(&selfFence, "self-fence-after", 25*time.Second, "maximum unproven lease duration before stopping workloads")
	flag.BoolVar(&insecure, "insecure-registry", false, "allow development registries over HTTP")
	flag.StringVar(&etcdTLSCert, "etcd-tls-cert", "", "etcd client TLS certificate")
	flag.StringVar(&etcdTLSKey, "etcd-tls-key", "", "etcd client TLS private key")
	flag.StringVar(&etcdCA, "etcd-ca", "", "etcd server CA certificate")
	flag.StringVar(&etcdServerName, "etcd-tls-server-name", "", "expected etcd certificate name")
	flag.BoolVar(&insecureEtcd, "insecure-etcd", false, "disable etcd TLS (development only)")
	flag.StringVar(&operationsListen, "operations-listen", "", "authenticated node operations listen address")
	flag.StringVar(&tlsCert, "tls-cert", "", "node server TLS certificate")
	flag.StringVar(&tlsKey, "tls-key", "", "node server TLS private key")
	flag.StringVar(&clientCA, "client-ca", "", "CA used to authenticate operations clients")
	flag.StringVar(&execHelper, "exec-helper", "/usr/libexec/glider-exec", "absolute path to hardened exec helper")
	flag.Parse()
	if nodeID == "" {
		fmt.Fprintln(os.Stderr, "gliderd: --node-id is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	etcdConfig := clientv3.Config{Endpoints: split(endpoints), DialTimeout: 5 * time.Second}
	var err error
	if !insecureEtcd {
		etcdConfig.TLS, err = transport.EtcdTLSConfig(etcdTLSCert, etcdTLSKey, etcdCA, etcdServerName)
		if err != nil {
			fatal(err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "gliderd: WARNING: etcd TLS disabled")
	}
	client, err := clientv3.New(etcdConfig)
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
	if err := driver.SetExecHelper(execHelper); err != nil {
		fatal(err)
	}
	if operationsListen != "" {
		creds, err := transport.ServerCredentials(tlsCert, tlsKey, clientCA)
		if err != nil {
			fatal(err)
		}
		listener, err := net.Listen("tcp", operationsListen)
		if err != nil {
			fatal(err)
		}
		operations, err := nodeops.New(nodeID, store, driver)
		if err != nil {
			fatal(err)
		}
		server := grpc.NewServer(grpc.Creds(creds), grpc.ChainUnaryInterceptor(transport.UnaryAuthorizationInterceptor(), transport.NewRateLimiter(20, 40).UnaryInterceptor()))
		nodeops.Register(server, operations)
		go func() { <-ctx.Done(); server.GracefulStop() }()
		go func() {
			if err := server.Serve(listener); err != nil && ctx.Err() == nil {
				fatal(err)
			}
		}()
		fmt.Fprintf(os.Stderr, "gliderd: node operations listening on %s\n", listener.Addr())
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
	if err != nil {
		fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	errs := make(chan error, 2)
	go func() { errs <- daemon.Run(runCtx) }()
	go func() { _ = agent.NewHealthDaemon(nodeID, store, driver, time.Second).Run(runCtx) }()
	go reconcileOverlay(runCtx, nodeID, store, driver, resync)
	go func() {
		errs <- leaseManager.Run(ctx, func(context.Context) error {
			cancelRun()
			fenceCtx, cancel := context.WithTimeout(context.Background(), selfFence)
			defer cancel()
			return reconciler.Reconcile(fenceCtx, nil)
		})
	}()
	err = <-errs
	cancelRun()
	if err != nil && ctx.Err() == nil {
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
func fatal(err error) { fmt.Fprintln(os.Stderr, "gliderd:", err); os.Exit(1) }

type nodeLister interface {
	ListNodes(context.Context) ([]api.Node, error)
}

func reconcileOverlay(ctx context.Context, nodeID string, store nodeLister, driver *agent.RuntimeDriver, period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		nodes, err := store.ListNodes(ctx)
		if err == nil {
			var local api.Node
			var peers []api.Node
			for _, node := range nodes {
				if node.Metadata.ID == nodeID {
					local = node
				} else {
					peers = append(peers, node)
				}
			}
			if local.Spec.TunnelAddress != "" {
				_ = driver.EnsureOverlay(local.Spec.TunnelAddress, peers, 1450)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
