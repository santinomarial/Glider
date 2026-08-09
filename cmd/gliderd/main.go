//go:build linux

package main

import (
	"context"
	"errors"
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
	imagemanager "github.com/santinomarial/glider/internal/image/manager"
	"github.com/santinomarial/glider/internal/lease"
	"github.com/santinomarial/glider/internal/nodeops"
	"github.com/santinomarial/glider/internal/observability"
	storeapi "github.com/santinomarial/glider/internal/store"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	"github.com/santinomarial/glider/internal/transport"
	"github.com/santinomarial/glider/internal/version"
)

var daemonLog = observability.NewLogger(os.Stderr, "gliderd")

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
	var controlplaneEndpoint, controlplaneCA, controlplaneServerName string
	var imageGCInterval, imageGCGrace time.Duration
	var minFreeBytes uint64
	var minFreePercent float64
	var showVersion bool
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
	flag.StringVar(&controlplaneEndpoint, "controlplane-endpoint", "", "control-plane address used for assignment-fenced secret delivery")
	flag.StringVar(&controlplaneCA, "controlplane-ca", "", "control-plane CA certificate")
	flag.StringVar(&controlplaneServerName, "controlplane-tls-server-name", "", "expected control-plane certificate name")
	flag.DurationVar(&imageGCInterval, "image-gc-interval", 15*time.Minute, "interval for reference-safe image garbage collection (zero disables periodic GC)")
	flag.DurationVar(&imageGCGrace, "image-gc-grace", 24*time.Hour, "minimum age before unreferenced image data is reclaimed")
	flag.Uint64Var(&minFreeBytes, "storage-min-free-bytes", 2<<30, "refuse new launches below this available space after GC")
	flag.Float64Var(&minFreePercent, "storage-min-free-percent", 10, "refuse new launches below this available filesystem percentage after GC")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
		fmt.Println(version.Version)
		return
	}
	if nodeID == "" {
		daemonLog.Error("node ID is required", nil)
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
		daemonLog.Warn("etcd TLS disabled", map[string]any{"insecure": true})
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
	if controlplaneEndpoint != "" {
		credentials, err := transport.ClientCredentials(tlsCert, tlsKey, controlplaneCA, controlplaneServerName)
		if err != nil {
			fatal(err)
		}
		resolver, err := agent.NewControlPlaneSecretResolver(controlplaneEndpoint, credentials)
		if err != nil {
			fatal(err)
		}
		defer resolver.Close()
		driver.SetSecretResolver(resolver)
	}
	if err := driver.ConfigureStorage(imageGCGrace, minFreeBytes, minFreePercent); err != nil {
		fatal(err)
	}
	if imageGCInterval < 0 {
		fatal(errors.New("image GC interval cannot be negative"))
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
		daemonLog.Info("node operations listening", map[string]any{"address": listener.Addr().String(), "node_id": nodeID})
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
	go reconcileNetworking(runCtx, nodeID, store, driver, resync)
	if imageGCInterval > 0 {
		go monitorStorage(runCtx, nodeID, store, driver, imageGCInterval)
	}
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

type storageDriver interface {
	CollectImages(context.Context) (imagemanager.GCResult, error)
	DiskUsage() (total, available uint64, pressured bool, err error)
}

type storageStore interface {
	GetNode(context.Context, string) (api.Node, error)
	PutNode(context.Context, api.Node, int64) (api.Node, error)
	EvictNodeAssignments(context.Context, string) error
	PutEvent(context.Context, api.Event) (api.Event, error)
}

func monitorStorage(ctx context.Context, nodeID string, store storageStore, driver storageDriver, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if result, err := driver.CollectImages(ctx); err != nil && ctx.Err() == nil {
			daemonLog.Error("image collection failed", map[string]any{"error": err.Error(), "node_id": nodeID})
		} else if result.BytesReclaimed > 0 {
			daemonLog.Info("image collection reclaimed storage", map[string]any{"bytes": result.BytesReclaimed, "blobs": result.BlobsRemoved, "layers": result.LayersRemoved, "node_id": nodeID})
		}
		total, available, pressured, err := driver.DiskUsage()
		if err != nil && ctx.Err() == nil {
			daemonLog.Error("storage pressure check failed", map[string]any{"error": err.Error(), "node_id": nodeID})
		} else if pressured {
			if err := evacuateDiskPressure(ctx, nodeID, store, total, available); err != nil && ctx.Err() == nil {
				daemonLog.Error("storage-pressure evacuation failed", map[string]any{"error": err.Error(), "node_id": nodeID})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func evacuateDiskPressure(ctx context.Context, nodeID string, store storageStore, total, available uint64) error {
	var node api.Node
	var err error
	firstTransition := true
	for attempt := 0; attempt < 5; attempt++ {
		node, err = store.GetNode(ctx, nodeID)
		if err != nil {
			return err
		}
		if node.Status.StoragePressure {
			firstTransition = false
		}
		node.Spec.Unschedulable = true
		node.Status.Phase = api.NodeDraining
		node.Status.StoragePressure = true
		node.Status.StorageTotalBytes = total
		node.Status.StorageAvailableBytes = available
		node.Status.UpdatedAt = time.Now().UTC()
		if _, err = store.PutNode(ctx, node, node.Metadata.Revision); err == nil {
			break
		}
		if !errors.Is(err, storeapi.ErrConflict) {
			return err
		}
	}
	if err != nil {
		return err
	}
	if err = store.EvictNodeAssignments(ctx, nodeID); err != nil {
		return err
	}
	if !firstTransition {
		return nil
	}
	event := api.Event{Metadata: api.Metadata{ID: uuid.NewString()}, Time: time.Now().UTC(), Type: "Warning", Reason: "StoragePressureEviction", Message: "node cordoned and assignments evicted after image GC could not restore the storage reserve", ObjectKind: "Node", ObjectID: nodeID, NodeID: nodeID, Fields: map[string]any{"total_bytes": total, "available_bytes": available}}
	_, err = store.PutEvent(ctx, event)
	return err
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
func fatal(err error) {
	daemonLog.Error("fatal process error", map[string]any{"error": err.Error()})
	os.Exit(1)
}

type nodeLister interface {
	ListNodes(context.Context) ([]api.Node, error)
	ListServices(context.Context) ([]api.Service, error)
}

func reconcileNetworking(ctx context.Context, nodeID string, store nodeLister, driver *agent.RuntimeDriver, period time.Duration) {
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
		if services, err := store.ListServices(ctx); err == nil {
			_ = driver.EnsureServices(services)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
