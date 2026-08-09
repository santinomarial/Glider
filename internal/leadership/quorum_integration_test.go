package leadership

import (
	"context"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

func TestThreeMemberQuorumSurvivesRaftLeaderLoss(t *testing.T) {
	client, servers, endpoints := startEtcdCluster(t, 3)
	root, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var active, maximum atomic.Int32
	leaders := make(chan string, 8)
	done := make(chan struct{}, 2)
	for _, id := range []string{"replica-a", "replica-b"} {
		go func(id string) {
			defer func() { done <- struct{}{} }()
			_ = Run(root, client, "/test/quorum-election", id, func(leaderCtx context.Context) error {
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
	awaitLeader(t, leaders)

	failed, oldLeader := raftLeader(t, client, endpoints)
	servers[failed].Close()
	servers[failed] = nil

	awaitNewRaftLeader(t, client, endpoints, oldLeader)
	operationCtx, operationCancel := context.WithTimeout(root, 10*time.Second)
	defer operationCancel()
	if _, err := client.Put(operationCtx, "/test/quorum-write", "survived"); err != nil {
		t.Fatalf("write after raft leader loss: %v", err)
	}
	response, err := client.Get(operationCtx, "/test/quorum-write")
	if err != nil {
		t.Fatalf("read after raft leader loss: %v", err)
	}
	if len(response.Kvs) != 1 || string(response.Kvs[0].Value) != "survived" {
		t.Fatalf("unexpected quorum read: %+v", response.Kvs)
	}
	if maximum.Load() != 1 || active.Load() != 1 {
		t.Fatalf("controller authority active=%d maximum=%d", active.Load(), maximum.Load())
	}

	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("leadership runner shutdown timeout")
		}
	}
}

func startEtcdCluster(t *testing.T, size int) (*clientv3.Client, []*embed.Etcd, []string) {
	t.Helper()
	clientURLs := make([]string, size)
	peerURLs := make([]string, size)
	initialCluster := ""
	for i := range size {
		clientURL := freeURL(t)
		peerURL := freeURL(t)
		clientURLs[i] = clientURL.String()
		peerURLs[i] = peerURL.String()
		if i > 0 {
			initialCluster += ","
		}
		initialCluster += fmt.Sprintf("member-%d=%s", i, peerURLs[i])
	}
	servers := make([]*embed.Etcd, size)
	for i := range size {
		cfg := embed.NewConfig()
		cfg.Name = fmt.Sprintf("member-%d", i)
		cfg.Dir, cfg.LogLevel, cfg.Logger = t.TempDir(), "error", "zap"
		cfg.ListenClientUrls = []url.URL{mustURL(t, clientURLs[i])}
		cfg.AdvertiseClientUrls = cfg.ListenClientUrls
		cfg.ListenPeerUrls = []url.URL{mustURL(t, peerURLs[i])}
		cfg.AdvertisePeerUrls = cfg.ListenPeerUrls
		cfg.InitialCluster = initialCluster
		cfg.InitialClusterToken = "glider-ha-test"
		server, err := embed.StartEtcd(cfg)
		if err != nil {
			t.Fatal(err)
		}
		servers[i] = server
	}
	for _, server := range servers {
		select {
		case <-server.Server.ReadyNotify():
		case <-time.After(15 * time.Second):
			t.Fatal("etcd cluster startup timeout")
		}
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: clientURLs, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		for _, server := range servers {
			if server != nil {
				server.Close()
			}
		}
	})
	return client, servers, clientURLs
}

func raftLeader(t *testing.T, client *clientv3.Client, endpoints []string) (int, uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, endpoint := range endpoints {
		status, err := client.Status(ctx, endpoint)
		if err == nil && status.Header.MemberId == status.Leader {
			return i, status.Leader
		}
	}
	t.Fatal("could not identify raft leader")
	return -1, 0
}

func awaitNewRaftLeader(t *testing.T, client *clientv3.Client, endpoints []string, oldLeader uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, endpoint := range endpoints {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			status, err := client.Status(ctx, endpoint)
			cancel()
			if err == nil && status.Leader != 0 && status.Leader != oldLeader {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("etcd quorum did not elect a replacement raft leader")
}

func mustURL(t *testing.T, value string) url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return *parsed
}
