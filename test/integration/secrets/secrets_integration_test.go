package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/controlplane"
	"github.com/santinomarial/glider/internal/pki"
	secretapi "github.com/santinomarial/glider/internal/secret"
	storeapi "github.com/santinomarial/glider/internal/store"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
	"github.com/santinomarial/glider/internal/transport"
)

func TestAssignmentFencedSecretDeliveryOverMTLS(t *testing.T) {
	etcd := startEtcd(t)
	store, _ := etcdstore.New(etcd, "secret-delivery")
	cipher, _ := secretapi.NewCipher(bytes.Repeat([]byte{9}, 32), "secret-delivery")
	ctx := context.Background()
	envelope, _ := cipher.Encrypt(api.Secret{Metadata: api.Metadata{ID: "database"}, Data: map[string][]byte{"password": []byte("delivered-value")}})
	savedSecret, err := store.PutSecret(ctx, envelope, 0)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "task"}, Spec: api.TaskSpec{Image: "image", Secrets: []api.SecretEnvRef{{SecretID: "database", Key: "password", Env: "PASSWORD"}}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	node, _ := store.PutNode(ctx, api.Node{Metadata: api.Metadata{ID: "node-a"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000}}, Status: api.NodeStatus{Phase: api.NodeReady}}, 0)
	assignment, err := store.Bind(ctx, storeapi.BindRequest{TaskID: task.Metadata.ID, TaskRevision: task.Metadata.Revision, NodeID: node.Metadata.ID, NodeRevision: node.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}

	serverAddress, dial := startControlPlane(t, store, cipher)
	nodeA := dial("node-a", serverAddress)
	defer nodeA.Close()
	out := new(structpb.Struct)
	in, _ := structpb.NewStruct(map[string]any{"task_id": "task", "generation": assignment.Generation})
	if err := nodeA.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", in, out); err != nil {
		t.Fatal(err)
	}
	values := out.AsMap()["values"].(map[string]any)
	decoded, _ := base64.StdEncoding.DecodeString(values["PASSWORD"].(string))
	if string(decoded) != "delivered-value" {
		t.Fatalf("secret = %q", decoded)
	}
	rotated, _ := cipher.Encrypt(api.Secret{Metadata: api.Metadata{ID: "database"}, Data: map[string][]byte{"password": []byte("rotated-value")}})
	if _, err := store.PutSecret(ctx, rotated, savedSecret.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.EvictNodeAssignments(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.GetTask(ctx, "task")
	currentNode, _ := store.GetNode(ctx, "node-a")
	next, err := store.Bind(ctx, storeapi.BindRequest{TaskID: "task", TaskRevision: pending.Metadata.Revision, NodeID: "node-a", NodeRevision: currentNode.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeA.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", in, new(structpb.Struct)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("superseded generation error = %v", err)
	}
	nextInput, _ := structpb.NewStruct(map[string]any{"task_id": "task", "generation": next.Generation})
	nextOutput := new(structpb.Struct)
	if err := nodeA.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", nextInput, nextOutput); err != nil {
		t.Fatal(err)
	}
	nextValues := nextOutput.AsMap()["values"].(map[string]any)
	nextDecoded, _ := base64.StdEncoding.DecodeString(nextValues["PASSWORD"].(string))
	if string(nextDecoded) != "rotated-value" {
		t.Fatalf("rotated secret = %q", nextDecoded)
	}

	stale, _ := structpb.NewStruct(map[string]any{"task_id": "task", "generation": next.Generation + 1})
	if err := nodeA.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", stale, new(structpb.Struct)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stale generation error = %v", err)
	}
	peer := dial("node-b", serverAddress)
	defer peer.Close()
	if err := peer.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", in, new(structpb.Struct)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("peer node error = %v", err)
	}
	events, _ := store.ListEvents(ctx)
	if len(events) != 2 || events[0].Reason != "SecretDelivered" || events[1].Reason != "SecretDelivered" || events[0].NodeID != "node-a" {
		t.Fatalf("audit events = %+v", events)
	}
}

func TestKeyLossAndCorruptCiphertextFailClosedUntilRecovery(t *testing.T) {
	etcd := startEtcd(t)
	store, _ := etcdstore.New(etcd, "secret-recovery")
	goodCipher, _ := secretapi.NewCipher(bytes.Repeat([]byte{4}, 32), "secret-recovery")
	wrongCipher, _ := secretapi.NewCipher(bytes.Repeat([]byte{5}, 32), "secret-recovery")
	ctx := context.Background()
	plaintext := api.Secret{Metadata: api.Metadata{ID: "recovery-secret"}, Data: map[string][]byte{"token": []byte("recovered-value")}}
	envelope, err := goodCipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.PutSecret(ctx, envelope, 0)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "recovery-task"}, Spec: api.TaskSpec{Image: "image", Secrets: []api.SecretEnvRef{{SecretID: "recovery-secret", Key: "token", Env: "TOKEN"}}}, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
	node, _ := store.PutNode(ctx, api.Node{Metadata: api.Metadata{ID: "recovery-node"}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 1000}}, Status: api.NodeStatus{Phase: api.NodeReady}}, 0)
	assignment, err := store.Bind(ctx, storeapi.BindRequest{TaskID: task.Metadata.ID, TaskRevision: task.Metadata.Revision, NodeID: node.Metadata.ID, NodeRevision: node.Metadata.Revision})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := structpb.NewStruct(map[string]any{"task_id": task.Metadata.ID, "generation": assignment.Generation})

	wrongAddress, wrongDial := startControlPlane(t, store, wrongCipher)
	wrongKeyNode := wrongDial(node.Metadata.ID, wrongAddress)
	defer wrongKeyNode.Close()
	if err := wrongKeyNode.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", input, new(structpb.Struct)); status.Code(err) != codes.Internal {
		t.Fatalf("replacement key did not fail closed: %v", err)
	}
	if events, _ := store.ListEvents(ctx); len(events) != 0 {
		t.Fatalf("failed decrypt emitted delivery audit: %+v", events)
	}

	corrupt := saved
	corrupt.Ciphertext = append([]byte(nil), saved.Ciphertext...)
	corrupt.Ciphertext[len(corrupt.Ciphertext)/2] ^= 1
	corrupt, err = store.PutSecret(ctx, corrupt, saved.Metadata.Revision)
	if err != nil {
		t.Fatal(err)
	}
	goodAddress, goodDial := startControlPlane(t, store, goodCipher)
	goodKeyNode := goodDial(node.Metadata.ID, goodAddress)
	defer goodKeyNode.Close()
	if err := goodKeyNode.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", input, new(structpb.Struct)); status.Code(err) != codes.Internal {
		t.Fatalf("corrupt ciphertext did not fail closed: %v", err)
	}

	restoredEnvelope, _ := goodCipher.Encrypt(plaintext)
	if _, err := store.PutSecret(ctx, restoredEnvelope, corrupt.Metadata.Revision); err != nil {
		t.Fatal(err)
	}
	output := new(structpb.Struct)
	if err := goodKeyNode.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", input, output); err != nil {
		t.Fatalf("delivery did not recover after restoring authenticated ciphertext: %v", err)
	}
	encoded := output.AsMap()["values"].(map[string]any)["TOKEN"].(string)
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	if string(decoded) != "recovered-value" {
		t.Fatalf("recovered secret = %q", decoded)
	}
	if events, _ := store.ListEvents(ctx); len(events) != 1 || events[0].Reason != "SecretDelivered" {
		t.Fatalf("recovery audit events = %+v", events)
	}
}

func startControlPlane(t *testing.T, store *etcdstore.Store, cipher *secretapi.Cipher) (string, func(string, string) *grpc.ClientConn) {
	t.Helper()
	dir := t.TempDir()
	caCert, caKey := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := pki.InitCA(caCert, caKey, "secret-delivery"); err != nil {
		t.Fatal(err)
	}
	serverCert, serverKey := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err := pki.Issue(serverCert, serverKey, caCert, caKey, pki.IssueOptions{Name: "controlplane", Role: "admin", ClusterID: "secret-delivery", DNSNames: []string{"controlplane"}, Server: true}); err != nil {
		t.Fatal(err)
	}
	serverCreds, _ := transport.ServerCredentials(serverCert, serverKey, caCert)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(serverCreds), grpc.UnaryInterceptor(transport.UnaryAuthorizationInterceptor()))
	service, _ := controlplane.New(store, cipher)
	controlplane.Register(server, service)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	dial := func(name, address string) *grpc.ClientConn {
		cert, key := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
		if err := pki.Issue(cert, key, caCert, caKey, pki.IssueOptions{Name: name, Role: "node", ClusterID: "secret-delivery", Client: true}); err != nil {
			t.Fatal(err)
		}
		creds, _ := transport.ClientCredentials(cert, key, caCert, "controlplane")
		conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	return listener.Addr().String(), dial
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
