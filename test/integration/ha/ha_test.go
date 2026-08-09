package haintegration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	etcdtransport "go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/pki"
	"github.com/santinomarial/glider/internal/transport"
)

func TestPackagedReplicasServeThroughControllerLeaderDeath(t *testing.T) {
	binary := os.Getenv("GLIDER_HA_CONTROLPLANE")
	if binary == "" {
		t.Skip("GLIDER_HA_CONTROLPLANE is required for packaged HA qualification")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("packaged control-plane path must be absolute")
	}
	files := createPKI(t)
	endpoint, client, stopEtcd := startEtcd(t, files)
	defer stopEtcd()
	replicaA := startReplica(t, binary, "replica-a", endpoint, files)
	replicaB := startReplica(t, binary, "replica-b", endpoint, files)
	defer replicaA.stop()
	defer replicaB.stop()

	saved := putTask(t, replicaA.address, "ha-canary")
	if task := getTask(t, replicaB.address, "ha-canary"); task.Metadata.Revision != saved.Metadata.Revision {
		t.Fatalf("replica state mismatch: %d != %d", task.Metadata.Revision, saved.Metadata.Revision)
	}
	leader := awaitLeader(t, client, "")
	var survivor *replica
	switch leader {
	case "replica-a":
		replicaA.stop()
		survivor = replicaB
	case "replica-b":
		replicaB.stop()
		survivor = replicaA
	default:
		t.Fatalf("unexpected controller leader %q", leader)
	}
	if task := getTask(t, survivor.address, "ha-canary"); task.Metadata.ID != "ha-canary" {
		t.Fatalf("surviving API lost state: %+v", task)
	}
	if next := awaitLeader(t, client, leader); next == leader {
		t.Fatalf("controller authority did not transfer from %s", leader)
	}
	deleteTask(t, survivor.address, saved)
}

func TestPackagedReplicasSurviveControllerCrashStorm(t *testing.T) {
	binary := os.Getenv("GLIDER_HA_CONTROLPLANE")
	if binary == "" {
		t.Skip("GLIDER_HA_CONTROLPLANE is required for packaged HA qualification")
	}
	files := createPKI(t)
	endpoint, client, stopEtcd := startEtcd(t, files)
	defer stopEtcd()
	replicas := map[string]*replica{}
	for _, identity := range []string{"storm-a", "storm-b", "storm-c"} {
		replicas[identity] = startReplica(t, binary, identity, endpoint, files)
	}
	defer func() {
		for _, running := range replicas {
			running.stop()
		}
	}()
	putTask(t, replicas["storm-a"].address, "storm-canary")

	previous := ""
	for round := 0; round < 5; round++ {
		leader := awaitLeader(t, client, previous)
		failed := replicas[leader]
		failed.stop()
		started := time.Now()
		next := awaitLeader(t, client, leader)
		if elapsed := time.Since(started); elapsed > 12*time.Second {
			t.Fatalf("round %d failover SLO exceeded: %s", round+1, elapsed)
		}
		if task := getTask(t, replicas[next].address, "storm-canary"); task.Metadata.ID != "storm-canary" {
			t.Fatalf("round %d lost durable API state: %+v", round+1, task)
		}
		replicas[leader] = startReplica(t, binary, leader, endpoint, files)
		previous = leader
	}
}

type tlsFiles struct{ ca, serverCert, serverKey, clientCert, clientKey, secretKey string }

func createPKI(t *testing.T) tlsFiles {
	t.Helper()
	directory := t.TempDir()
	files := tlsFiles{ca: filepath.Join(directory, "ca.crt"), serverCert: filepath.Join(directory, "etcd.crt"), serverKey: filepath.Join(directory, "etcd.key"), clientCert: filepath.Join(directory, "client.crt"), clientKey: filepath.Join(directory, "client.key"), secretKey: filepath.Join(directory, "secret.key")}
	caKey := filepath.Join(directory, "ca.key")
	if err := pki.InitCA(files.ca, caKey, "ha-test"); err != nil {
		t.Fatal(err)
	}
	if err := pki.Issue(files.serverCert, files.serverKey, files.ca, caKey, pki.IssueOptions{Name: "etcd", Role: "etcd-client", ClusterID: "ha-test", Server: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ValidFor: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := pki.Issue(files.clientCert, files.clientKey, files.ca, caKey, pki.IssueOptions{Name: "controlplane", Role: "etcd-client", ClusterID: "ha-test", Client: true, ValidFor: time.Hour}); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.secretKey, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return files
}

func startEtcd(t *testing.T, files tlsFiles) (string, *clientv3.Client, func()) {
	t.Helper()
	clientURL, peerURL := freeURL(t, "https"), freeURL(t, "http")
	config := embed.NewConfig()
	config.Dir, config.LogLevel, config.Logger = t.TempDir(), "error", "zap"
	config.ListenClientUrls, config.AdvertiseClientUrls = []url.URL{clientURL}, []url.URL{clientURL}
	config.ListenPeerUrls, config.AdvertisePeerUrls = []url.URL{peerURL}, []url.URL{peerURL}
	config.InitialCluster = "default=" + peerURL.String()
	config.ClientTLSInfo = etcdtransport.TLSInfo{CertFile: files.serverCert, KeyFile: files.serverKey, TrustedCAFile: files.ca, ClientCertAuth: true}
	server, err := embed.StartEtcd(config)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Server.ReadyNotify():
	case <-time.After(10 * time.Second):
		server.Close()
		t.Fatal("TLS etcd startup timeout")
	}
	tlsConfig, err := transport.EtcdTLSConfig(files.clientCert, files.clientKey, files.ca, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{clientURL.String()}, DialTimeout: 3 * time.Second, TLS: tlsConfig})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return clientURL.String(), client, func() { client.Close(); server.Close() }
}

type replica struct {
	identity string
	address  string
	command  *exec.Cmd
	output   strings.Builder
	mu       sync.Mutex
	stopped  bool
}

func startReplica(t *testing.T, binary, identity, endpoint string, files tlsFiles) *replica {
	t.Helper()
	result := &replica{identity: identity, address: freeURL(t, "http").Host}
	result.command = exec.Command(binary, "--listen="+result.address, "--instance-id="+identity, "--cluster-id=ha-test", "--etcd-endpoints="+endpoint, "--etcd-tls-cert="+files.clientCert, "--etcd-tls-key="+files.clientKey, "--etcd-ca="+files.ca, "--etcd-tls-server-name=127.0.0.1", "--secret-key-file="+files.secretKey, "--insecure-development", "--metrics-listen=", "--dns-listen=")
	result.command.Stdout, result.command.Stderr = &result.output, &result.output
	if err := result.command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := listTasks(result.address); err == nil {
			return result
		}
		time.Sleep(50 * time.Millisecond)
	}
	result.stop()
	t.Fatalf("replica %s failed startup: %s", identity, result.output.String())
	return nil
}

func (r *replica) stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()
	_ = r.command.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = r.command.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = r.command.Process.Kill()
		<-done
	}
}

func awaitLeader(t *testing.T, client *clientv3.Client, previous string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, err := client.Get(ctx, "/glider/v1/clusters/ha-test/elections/controllers", clientv3.WithPrefix())
		cancel()
		if err == nil && len(response.Kvs) > 0 {
			leaderKV := response.Kvs[0]
			for _, candidate := range response.Kvs[1:] {
				if candidate.CreateRevision < leaderKV.CreateRevision {
					leaderKV = candidate
				}
			}
			leader := string(leaderKV.Value)
			if leader != "" && leader != previous {
				return leader
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("controller election did not converge")
	return ""
}

func putTask(t *testing.T, address, id string) api.Task {
	t.Helper()
	task := api.Task{Metadata: api.Metadata{ID: id, IdempotencyKey: id + "-create"}, Spec: api.TaskSpec{Image: "example.invalid/ha:1"}, Status: api.TaskStatus{Phase: api.TaskPending}}
	out, err := call(address, "PutTask", mustStruct(t, task))
	if err != nil {
		t.Fatal(err)
	}
	var saved api.Task
	decode(t, out, &saved)
	return saved
}

func getTask(t *testing.T, address, id string) api.Task {
	t.Helper()
	request, _ := structpb.NewStruct(map[string]any{"id": id})
	out, err := call(address, "GetTask", request)
	if err != nil {
		t.Fatal(err)
	}
	var task api.Task
	decode(t, out, &task)
	return task
}

func deleteTask(t *testing.T, address string, task api.Task) {
	t.Helper()
	request, _ := structpb.NewStruct(map[string]any{"id": task.Metadata.ID, "revision": float64(task.Metadata.Revision)})
	if _, err := call(address, "DeleteTask", request); err != nil {
		t.Fatal(err)
	}
}

func listTasks(address string) (*structpb.Struct, error) {
	return call(address, "ListTasks", &structpb.Struct{})
}
func call(address, method string, input *structpb.Struct) (*structpb.Struct, error) {
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output := new(structpb.Struct)
	if err = connection.Invoke(ctx, "/glider.v1.ControlPlane/"+method, input, output); err != nil {
		return nil, err
	}
	return output, nil
}

func mustStruct(t *testing.T, value any) *structpb.Struct {
	t.Helper()
	data, _ := json.Marshal(value)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	result, err := structpb.NewStruct(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decode(t *testing.T, value *structpb.Struct, target any) {
	t.Helper()
	data, _ := json.Marshal(value.AsMap())
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func freeURL(t *testing.T, scheme string) url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	parsed, _ := url.Parse(fmt.Sprintf("%s://%s", scheme, address))
	return *parsed
}
