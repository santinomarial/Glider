package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	"github.com/santinomarial/glider/internal/observability"
	"github.com/santinomarial/glider/internal/scheduler"
	secretapi "github.com/santinomarial/glider/internal/secret"
	storeapi "github.com/santinomarial/glider/internal/store"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	"github.com/santinomarial/glider/internal/transport"
	"github.com/santinomarial/glider/internal/version"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "gRPC listen address")
	endpoints := flag.String("etcd-endpoints", "127.0.0.1:2379", "comma-separated etcd endpoints")
	clusterID := flag.String("cluster-id", "default", "Glider cluster ID")
	dnsListen := flag.String("dns-listen", "", "optional authoritative cluster DNS UDP address (for example :53)")
	metricsListen := flag.String("metrics-listen", "127.0.0.1:9090", "Prometheus metrics listen address; empty disables")
	tlsCert := flag.String("tls-cert", "", "server TLS certificate")
	tlsKey := flag.String("tls-key", "", "server TLS private key")
	clientCA := flag.String("client-ca", "", "CA used to authenticate client certificates")
	insecureDevelopment := flag.Bool("insecure-development", false, "disable TLS and authentication (development only)")
	etcdTLSCert := flag.String("etcd-tls-cert", "", "etcd client TLS certificate")
	etcdTLSKey := flag.String("etcd-tls-key", "", "etcd client TLS private key")
	etcdCA := flag.String("etcd-ca", "", "etcd server CA certificate")
	etcdServerName := flag.String("etcd-tls-server-name", "", "expected etcd certificate name")
	insecureEtcd := flag.Bool("insecure-etcd", false, "disable etcd TLS (development only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	requestRate := flag.Int("request-rate", 50, "allowed requests per second per authenticated principal")
	requestBurst := flag.Int("request-burst", 100, "request burst per authenticated principal")
	quotaTasks := flag.Int64("quota-tasks", 10000, "maximum cluster tasks")
	quotaWorkloads := flag.Int64("quota-workloads", 1000, "maximum cluster workloads")
	quotaServices := flag.Int64("quota-services", 1000, "maximum cluster services")
	quotaCPUMilli := flag.Int64("quota-cpu-milli", 1000000, "maximum aggregate requested CPU in millicores")
	quotaMemoryBytes := flag.Int64("quota-memory-bytes", 1<<50, "maximum aggregate requested memory")
	secretKeyFile := flag.String("secret-key-file", "", "path to 32-byte secret encryption key (required)")
	eventRetention := flag.Duration("event-retention", 7*24*time.Hour, "maximum event age")
	eventRetentionMax := flag.Int("event-retention-max", 100000, "maximum retained events")
	eventPruneInterval := flag.Duration("event-prune-interval", 5*time.Minute, "event retention reconciliation interval")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	if *eventRetention <= 0 || *eventRetentionMax <= 0 || *eventPruneInterval <= 0 {
		fatal(errors.New("event retention duration, maximum, and prune interval must be positive"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var err error
	etcdConfig := clientv3.Config{Endpoints: split(*endpoints), DialTimeout: 5 * time.Second}
	if !*insecureEtcd {
		etcdConfig.TLS, err = transport.EtcdTLSConfig(*etcdTLSCert, *etcdTLSKey, *etcdCA, *etcdServerName)
		if err != nil {
			fatal(err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "glider-controlplane: WARNING: etcd TLS disabled")
	}
	client, err := clientv3.New(etcdConfig)
	if err != nil {
		fatal(err)
	}
	defer client.Close()
	store, err := etcdstore.New(client, *clusterID)
	if err != nil {
		fatal(err)
	}
	if _, err := store.EnsureSchema(ctx, etcdstore.QuotaLimits{Tasks: *quotaTasks, Workloads: *quotaWorkloads, Services: *quotaServices, Resources: api.Resources{CPUMilli: *quotaCPUMilli, MemoryBytes: *quotaMemoryBytes}}); err != nil {
		fatal(err)
	}
	if *secretKeyFile == "" {
		fatal(errors.New("--secret-key-file is required"))
	}
	secretCipher, err := secretapi.LoadKeyFile(*secretKeyFile, *clusterID)
	if err != nil {
		fatal(err)
	}
	service, err := controlplane.New(store, secretCipher)
	if err != nil {
		fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fatal(err)
	}
	var serverOptions []grpc.ServerOption
	if *insecureDevelopment {
		fmt.Fprintln(os.Stderr, "glider-controlplane: WARNING: TLS and authorization disabled")
	} else {
		if *tlsCert == "" || *tlsKey == "" || *clientCA == "" {
			fatal(errors.New("--tls-cert, --tls-key, and --client-ca are required unless --insecure-development is set"))
		}
		credentials, err := transport.ServerCredentials(*tlsCert, *tlsKey, *clientCA)
		if err != nil {
			fatal(err)
		}
		limiter := transport.NewRateLimiter(*requestRate, *requestBurst)
		serverOptions = append(serverOptions, grpc.Creds(credentials), grpc.ChainUnaryInterceptor(transport.UnaryAuthorizationInterceptor(), limiter.UnaryInterceptor()))
	}
	serverOptions = append(serverOptions, grpc.MaxRecvMsgSize(1<<20), grpc.MaxSendMsgSize(4<<20), grpc.MaxConcurrentStreams(256))
	server := grpc.NewServer(serverOptions...)
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
	if *metricsListen != "" {
		metricsTLS, err := transport.ServerTLSConfig(*tlsCert, *tlsKey, *clientCA)
		if err != nil && !*insecureDevelopment {
			fatal(err)
		}
		metrics := &http.Server{Addr: *metricsListen, Handler: observability.NewMetricsHandler(store), ReadHeaderTimeout: 5 * time.Second, TLSConfig: metricsTLS}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = metrics.Shutdown(shutdown)
		}()
		go func() {
			var err error
			if *insecureDevelopment {
				err = metrics.ListenAndServe()
			} else {
				err = metrics.ListenAndServeTLS("", "")
			}
			if err != nil && err != http.ErrServerClosed {
				fatal(err)
			}
		}()
	}
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
	go pruneEvents(ctx, store, *eventRetention, *eventRetentionMax, *eventPruneInterval)
	go func() { _ = lease.NewMonitor(client, *clusterID, store, 20*time.Second, 2*time.Second).Run(ctx) }()
	go func() { <-ctx.Done(); server.GracefulStop() }()
	fmt.Fprintf(os.Stderr, "glider-controlplane: listening on %s\n", listener.Addr())
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func pruneEvents(ctx context.Context, store *etcdstore.Store, retention time.Duration, maximum int, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if removed, err := store.PruneEvents(ctx, time.Now().UTC().Add(-retention), maximum); err != nil && ctx.Err() == nil && !errors.Is(err, storeapi.ErrConflict) {
			fmt.Fprintf(os.Stderr, "glider-controlplane: event retention failed: %v\n", err)
		} else if removed > 0 {
			fmt.Fprintf(os.Stderr, "glider-controlplane: pruned %d expired events\n", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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
