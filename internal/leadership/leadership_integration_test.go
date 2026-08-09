package leadership

import (
	"context"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

func TestLeaderLossTransfersSingleControllerAuthority(t *testing.T) {
	client := startEtcd(t)
	root, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	contexts := make(map[string]context.CancelFunc)
	leaders := make(chan string, 4)
	var active, maximum atomic.Int32
	done := make(chan struct{}, 2)
	for _, id := range []string{"replica-a", "replica-b"} {
		ctx, stop := context.WithCancel(root)
		contexts[id] = stop
		go func(id string) {
			defer func() { done <- struct{}{} }()
			_ = Run(ctx, client, "/test/election", id, func(leaderCtx context.Context) error {
				current := active.Add(1)
				defer active.Add(-1)
				for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
				}
				leaders <- id
				<-leaderCtx.Done()
				return leaderCtx.Err()
			})
		}(id)
	}
	first := awaitLeader(t, leaders)
	contexts[first]()
	second := awaitLeader(t, leaders)
	if second == first {
		t.Fatalf("leadership did not transfer: %s then %s", first, second)
	}
	if maximum.Load() != 1 {
		t.Fatalf("simultaneous controller authorities = %d", maximum.Load())
	}
	contexts[second]()
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("leadership runner shutdown timeout")
		}
	}
}

func awaitLeader(t *testing.T, leaders <-chan string) string {
	t.Helper()
	select {
	case id := <-leaders:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("leadership transfer timeout")
		return ""
	}
}

func startEtcd(t *testing.T) *clientv3.Client {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir, cfg.LogLevel, cfg.Logger = t.TempDir(), "error", "zap"
	cfg.ListenClientUrls = []url.URL{freeURL(t)}
	cfg.AdvertiseClientUrls = cfg.ListenClientUrls
	cfg.ListenPeerUrls = []url.URL{freeURL(t)}
	cfg.AdvertisePeerUrls = cfg.ListenPeerUrls
	cfg.InitialCluster = "default=" + cfg.ListenPeerUrls[0].String()
	server, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		t.Fatal("etcd startup timeout")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{cfg.ListenClientUrls[0].String()}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client
}

func freeURL(t *testing.T) url.URL {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	value, _ := url.Parse("http://" + address)
	return *value
}
