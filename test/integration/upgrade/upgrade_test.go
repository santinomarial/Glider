package upgradeintegration

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
	"testing"
	"time"

	etcdtransport "go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/pki"
)

func TestPackagedCanaryMigrationAndLegacyRollback(t *testing.T) {
	currentControl := requiredBinary(t, "GLIDER_UPGRADE_CURRENT_CONTROLPLANE")
	currentAdmin := requiredBinary(t, "GLIDER_UPGRADE_CURRENT_ADMIN")
	legacyControl := requiredBinary(t, "GLIDER_UPGRADE_LEGACY_CONTROLPLANE")
	pkiFiles := createPKI(t)
	endpoint, stopEtcd := startTLSEtcd(t, pkiFiles)
	defer stopEtcd()

	runAdmin(t, currentAdmin, pkiFiles, endpoint, "migrate")
	status := runAdmin(t, currentAdmin, pkiFiles, endpoint, "status")
	if !strings.Contains(status, `"version": 2`) || !strings.Contains(status, `"minimum_writer": 2`) {
		t.Fatalf("unexpected migrated schema: %s", status)
	}
	current := startControlPlane(t, currentControl, pkiFiles, endpoint)
	canaryTaskLifecycle(t, current.address, "current-canary")
	current.stop(t)

	runAdmin(t, currentAdmin, pkiFiles, endpoint, "downgrade", "--target=1")
	status = runAdmin(t, currentAdmin, pkiFiles, endpoint, "status")
	if !strings.Contains(status, `"version": 1`) || !strings.Contains(status, `"minimum_writer": 1`) {
		t.Fatalf("unexpected downgraded schema: %s", status)
	}
	legacy := startControlPlane(t, legacyControl, pkiFiles, endpoint)
	canaryTaskLifecycle(t, legacy.address, "legacy-canary")
	legacy.stop(t)
}

type testPKI struct{ ca, serverCert, serverKey, clientCert, clientKey, secretKey string }

func createPKI(t *testing.T) testPKI {
	t.Helper()
	directory := t.TempDir()
	files := testPKI{ca: filepath.Join(directory, "ca.crt"), serverCert: filepath.Join(directory, "etcd.crt"), serverKey: filepath.Join(directory, "etcd.key"), clientCert: filepath.Join(directory, "client.crt"), clientKey: filepath.Join(directory, "client.key"), secretKey: filepath.Join(directory, "secret.key")}
	caKey := filepath.Join(directory, "ca.key")
	if err := pki.InitCA(files.ca, caKey, "upgrade-test"); err != nil {
		t.Fatal(err)
	}
	if err := pki.Issue(files.serverCert, files.serverKey, files.ca, caKey, pki.IssueOptions{Name: "etcd", Role: "etcd-client", ClusterID: "upgrade-test", Server: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ValidFor: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := pki.Issue(files.clientCert, files.clientKey, files.ca, caKey, pki.IssueOptions{Name: "upgrade", Role: "etcd-client", ClusterID: "upgrade-test", Client: true, ValidFor: time.Hour}); err != nil {
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

func startTLSEtcd(t *testing.T, files testPKI) (string, func()) {
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
	return clientURL.String(), server.Close
}

func runAdmin(t *testing.T, binary string, files testPKI, endpoint, operation string, extra ...string) string {
	t.Helper()
	arguments := []string{"schema", operation, "--endpoint=" + endpoint, "--cluster-id=upgrade-test", "--tls-cert=" + files.clientCert, "--tls-key=" + files.clientKey, "--ca=" + files.ca, "--tls-server-name=127.0.0.1"}
	arguments = append(arguments, extra...)
	command := exec.Command(binary, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("admin %s: %v: %s", operation, err, output)
	}
	return string(output)
}

type controlProcess struct {
	command *exec.Cmd
	address string
	output  strings.Builder
}

func startControlPlane(t *testing.T, binary string, files testPKI, endpoint string) *controlProcess {
	t.Helper()
	address := freeAddress(t)
	process := &controlProcess{address: address}
	arguments := []string{"--listen=" + address, "--cluster-id=upgrade-test", "--etcd-endpoints=" + endpoint, "--etcd-tls-cert=" + files.clientCert, "--etcd-tls-key=" + files.clientKey, "--etcd-ca=" + files.ca, "--etcd-tls-server-name=127.0.0.1", "--secret-key-file=" + files.secretKey, "--insecure-development", "--metrics-listen=", "--dns-listen="}
	if !strings.Contains(filepath.Base(binary), "legacy") {
		arguments = append(arguments, "--instance-id=packaged-canary")
	}
	process.command = exec.Command(binary, arguments...)
	process.command.Stdout, process.command.Stderr = &process.output, &process.output
	if err := process.command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			out := new(structpb.Struct)
			err = connection.Invoke(ctx, "/glider.v1.ControlPlane/ListTasks", &structpb.Struct{}, out)
			cancel()
			connection.Close()
			if err == nil {
				return process
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	process.stop(t)
	t.Fatalf("control plane did not become ready: %s", process.output.String())
	return nil
}

func (p *controlProcess) stop(t *testing.T) {
	t.Helper()
	if p.command.Process == nil {
		return
	}
	_ = p.command.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- p.command.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.command.Process.Kill()
		<-done
	}
	if !p.command.ProcessState.Success() && !strings.Contains(p.output.String(), "context canceled") {
		t.Logf("control-plane shutdown output: %s", p.output.String())
	}
}

func canaryTaskLifecycle(t *testing.T, address, id string) {
	t.Helper()
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task := api.Task{Metadata: api.Metadata{ID: id, IdempotencyKey: id + "-create"}, Spec: api.TaskSpec{Image: "example.invalid/canary:1"}, Status: api.TaskStatus{Phase: api.TaskPending}}
	in := mustStruct(t, task)
	out := new(structpb.Struct)
	if err = connection.Invoke(ctx, "/glider.v1.ControlPlane/PutTask", in, out); err != nil {
		t.Fatal(err)
	}
	var saved api.Task
	decodeStruct(t, out, &saved)
	request, _ := structpb.NewStruct(map[string]any{"id": id, "revision": float64(saved.Metadata.Revision)})
	if err = connection.Invoke(ctx, "/glider.v1.ControlPlane/DeleteTask", request, new(structpb.Struct)); err != nil {
		t.Fatal(err)
	}
}

func mustStruct(t *testing.T, value any) *structpb.Struct {
	t.Helper()
	data, _ := json.Marshal(value)
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	result, err := structpb.NewStruct(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeStruct(t *testing.T, value *structpb.Struct, target any) {
	t.Helper()
	data, _ := json.Marshal(value.AsMap())
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func requiredBinary(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Skipf("%s is required for packaged upgrade qualification", name)
	}
	if !filepath.IsAbs(value) {
		t.Fatalf("%s must be absolute", name)
	}
	return value
}

func freeAddress(t *testing.T) string { return freeURL(t, "http").Host }
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
