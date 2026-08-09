package backupintegration

import (
	"bytes"
	"context"
	gliderbackup "github.com/santinomarial/glider/internal/backup"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncryptedSnapshotRestoresAuthoritativeState(t *testing.T) {
	client, server := start(t, t.TempDir(), freeURL(t), freeURL(t))
	ctx := context.Background()
	if _, err := client.Put(ctx, "/glider/v1/clusters/test/tasks/task", `{"id":"task"}`); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := snapshot.NewV3(zap.NewNop()).Save(ctx, clientv3.Config{Endpoints: client.Endpoints(), DialTimeout: time.Second}, plain); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 64)
	encrypted := filepath.Join(t.TempDir(), "snapshot.glider")
	source, _ := os.Open(plain)
	destination, _ := os.Create(encrypted)
	if err := gliderbackup.Encrypt(destination, source, key); err != nil {
		t.Fatal(err)
	}
	source.Close()
	destination.Close()
	assertCorruptAndWrongKeyRejected(t, encrypted, key)
	decrypted := filepath.Join(t.TempDir(), "decrypted.db")
	source, _ = os.Open(encrypted)
	destination, _ = os.Create(decrypted)
	if err := gliderbackup.Decrypt(destination, source, key); err != nil {
		t.Fatal(err)
	}
	source.Close()
	destination.Close()
	client.Close()
	server.Close()
	dataDir := filepath.Join(t.TempDir(), "restored")
	peer := freeURL(t)
	if err := snapshot.NewV3(zap.NewNop()).Restore(snapshot.RestoreConfig{SnapshotPath: decrypted, Name: "default", OutputDataDir: dataDir, PeerURLs: []string{peer.String()}, InitialCluster: "default=" + peer.String(), InitialClusterToken: "restore", RevisionBump: 1_000_000, MarkCompacted: true}); err != nil {
		t.Fatal(err)
	}
	restored, restoredServer := start(t, dataDir, freeURL(t), peer)
	defer restored.Close()
	defer restoredServer.Close()
	response, err := restored.Get(ctx, "/glider/v1/clusters/test/tasks/task")
	if err != nil || len(response.Kvs) != 1 {
		t.Fatalf("restored state missing: response=%v err=%v", response, err)
	}
}

func assertCorruptAndWrongKeyRejected(t *testing.T, encrypted string, key []byte) {
	t.Helper()
	wrongKey := bytes.Repeat([]byte{8}, 64)
	source, err := os.Open(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err = gliderbackup.Decrypt(io.Discard, source, wrongKey); err == nil {
		source.Close()
		t.Fatal("backup encrypted with an unavailable key was accepted")
	}
	source.Close()

	data, err := os.ReadFile(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x80
	corrupt := filepath.Join(t.TempDir(), "corrupt.snapshot")
	if err = os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err = os.Open(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err = gliderbackup.Decrypt(io.Discard, source, key); err == nil {
		t.Fatal("corrupt backup was accepted")
	}
}
func start(t *testing.T, dir string, clientURL, peerURL url.URL) (*clientv3.Client, *embed.Etcd) {
	t.Helper()
	cfg := embed.NewConfig()
	cfg.Dir = dir
	cfg.LogLevel = "error"
	cfg.Logger = "zap"
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
		server.Close()
		t.Fatal("etcd not ready")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, DialTimeout: time.Second})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, server
}
func freeURL(t *testing.T) url.URL {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	value, err := url.Parse("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	return *value
}
