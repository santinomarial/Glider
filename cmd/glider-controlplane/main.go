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

	"github.com/santinomarial/glider/internal/controlplane"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "gRPC listen address")
	endpoints := flag.String("etcd-endpoints", "127.0.0.1:2379", "comma-separated etcd endpoints")
	clusterID := flag.String("cluster-id", "default", "Glider cluster ID")
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
