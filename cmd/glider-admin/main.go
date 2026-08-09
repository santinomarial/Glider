package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/santinomarial/glider/internal/backup"
	"github.com/santinomarial/glider/internal/transport"
	"github.com/santinomarial/glider/internal/version"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/etcdutl/v3/snapshot"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("usage: glider-admin backup|verify|restore|pki|secret-key"))
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)
		return
	case "backup":
		err = runBackup(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "restore":
		err = runRestore(os.Args[2:])
	case "pki":
		err = runPKI(os.Args[2:])
	case "secret-key":
		err = runSecretKey(os.Args[2:])
	default:
		err = errors.New("unknown command")
	}
	if err != nil {
		fatal(err)
	}
}

func runSecretKey(args []string) error {
	fs := flag.NewFlagSet("secret-key", flag.ContinueOnError)
	output := fs.String("output", "", "new 32-byte secret encryption key path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" || !filepath.IsAbs(*output) {
		return errors.New("--output must be an absolute path")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(key); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(*output)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(*output)
		return closeErr
	}
	fmt.Printf("secret_key=%s mode=0600 bytes=32\n", *output)
	return nil
}

type tlsFlags struct{ cert, key, ca, serverName *string }

func addTLS(fs *flag.FlagSet) *tlsFlags {
	return &tlsFlags{cert: fs.String("tls-cert", "", "etcd client certificate"), key: fs.String("tls-key", "", "etcd client key"), ca: fs.String("ca", "", "etcd CA"), serverName: fs.String("tls-server-name", "", "expected etcd certificate name")}
}
func (t *tlsFlags) config(endpoint string) (clientv3.Config, error) {
	tlsConfig, err := transport.EtcdTLSConfig(*t.cert, *t.key, *t.ca, *t.serverName)
	if err != nil {
		return clientv3.Config{}, err
	}
	return clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second, TLS: tlsConfig}, nil
}
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	endpoint := fs.String("endpoint", "", "single etcd member endpoint")
	output := fs.String("output", "", "encrypted backup output path")
	keyFile := fs.String("key-file", "", "64-byte backup encryption key")
	timeout := fs.Duration("timeout", 10*time.Minute, "snapshot timeout")
	tls := addTLS(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" || *output == "" || *keyFile == "" {
		return errors.New("--endpoint, --output, and --key-file are required")
	}
	config, err := tls.config(*endpoint)
	if err != nil {
		return err
	}
	key, err := backup.LoadKey(*keyFile)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(*output), ".glider-snapshot-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath)
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	version, err := snapshot.NewV3(zap.NewNop()).Save(ctx, config, tmpPath)
	if err != nil {
		return err
	}
	source, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = backup.Encrypt(destination, source, key); err == nil {
		err = destination.Sync()
	}
	closeErr := destination.Close()
	if err != nil {
		os.Remove(*output)
		return err
	}
	if closeErr != nil {
		os.Remove(*output)
		return closeErr
	}
	fmt.Printf("backup=%s etcd_version=%s encrypted=true\n", *output, version)
	return nil
}
func decrypted(snapshotPath, keyFile string) (string, func(), error) {
	key, err := backup.LoadKey(keyFile)
	if err != nil {
		return "", nil, err
	}
	source, err := os.Open(snapshotPath)
	if err != nil {
		return "", nil, err
	}
	defer source.Close()
	tmp, err := os.CreateTemp("", "glider-restore-*.db")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	if err = backup.Decrypt(tmp, source, key); err != nil {
		cleanup()
		return "", nil, err
	}
	if err = tmp.Sync(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err = tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}
func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "encrypted backup path")
	key := fs.String("key-file", "", "backup encryption key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, cleanup, err := decrypted(*input, *key)
	if err != nil {
		return err
	}
	defer cleanup()
	status, err := snapshot.NewV3(zap.NewNop()).Status(path)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(status, "", "  ")
	fmt.Println(string(data))
	return nil
}
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	input := fs.String("input", "", "encrypted backup path")
	key := fs.String("key-file", "", "backup encryption key")
	dataDir := fs.String("data-dir", "", "new etcd data directory")
	name := fs.String("name", "default", "etcd member name")
	peerURL := fs.String("peer-url", "http://127.0.0.1:2380", "member peer URL")
	cluster := fs.String("initial-cluster", "default=http://127.0.0.1:2380", "initial cluster mapping")
	token := fs.String("initial-cluster-token", "glider-restored", "new cluster token")
	revisionBump := fs.Uint64("revision-bump", 1_000_000_000, "revision bump protecting informer/watch clients")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *key == "" || *dataDir == "" {
		return errors.New("--input, --key-file, and --data-dir are required")
	}
	path, cleanup, err := decrypted(*input, *key)
	if err != nil {
		return err
	}
	defer cleanup()
	manager := snapshot.NewV3(zap.NewNop())
	if _, err = manager.Status(path); err != nil {
		return fmt.Errorf("verify snapshot: %w", err)
	}
	return manager.Restore(snapshot.RestoreConfig{SnapshotPath: path, Name: *name, OutputDataDir: *dataDir, PeerURLs: []string{*peerURL}, InitialCluster: *cluster, InitialClusterToken: *token, RevisionBump: *revisionBump, MarkCompacted: *revisionBump > 0})
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "glider-admin:", err); os.Exit(1) }
