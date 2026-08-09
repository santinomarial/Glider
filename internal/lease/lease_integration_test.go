package lease

import (
	"context"
	"errors"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"net"
	"net/url"
	"testing"
	"time"
)

func TestExclusiveNodeLease(t *testing.T) {
	client := startEtcd(t)
	first, _ := New(client, "cluster", "node", "owner-a", time.Second, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx, func(context.Context) error { return nil }) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		alive, err := NodeAlive(context.Background(), client, "cluster", "node")
		if err == nil && alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	second, _ := New(client, "cluster", "node", "owner-b", time.Second, 2*time.Second)
	err := second.Run(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrNodeOwned) {
		t.Fatalf("second owner error=%v", err)
	}
	cancel()
	<-done
}

func TestControlPlanePartitionSelfFencesWithinDeadline(t *testing.T) {
	client, stop := startEtcdWithStop(t)
	manager, err := New(client, "cluster", "partitioned-node", "owner", time.Second, 2500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	fenced := make(chan time.Time, 1)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- manager.Run(context.Background(), func(context.Context) error {
			fenced <- time.Now()
			return nil
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		alive, checkErr := NodeAlive(context.Background(), client, "cluster", "partitioned-node")
		if checkErr == nil && alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease was not acquired before partition")
		}
		time.Sleep(10 * time.Millisecond)
	}
	partitionedAt := time.Now()
	stop()
	select {
	case at := <-fenced:
		elapsed := at.Sub(partitionedAt)
		if elapsed > 4*time.Second {
			t.Fatalf("self-fencing SLO exceeded: %s", elapsed)
		}
		if at.Before(started) {
			t.Fatal("invalid fencing timestamp")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("partitioned node did not self-fence")
	}
	if err := <-done; err == nil {
		t.Fatal("lease manager returned success after partition")
	}
}

func startEtcd(t *testing.T) *clientv3.Client {
	client, _ := startEtcdWithStop(t)
	return client
}

func startEtcdWithStop(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
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
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("etcd start timeout")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		server.Close()
	}
	t.Cleanup(func() { client.Close(); stop() })
	return client, stop
}
func freeURL(t *testing.T) url.URL {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	u, err := url.Parse("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}
