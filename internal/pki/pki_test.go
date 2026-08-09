package pki

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIssueRoleIdentityAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := InitCA(caCert, caKey, "cluster"); err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(dir, "operator.crt"), filepath.Join(dir, "operator.key")
	options := IssueOptions{Name: "alice", Role: "operator", ClusterID: "cluster", Client: true, ValidFor: 24 * time.Hour}
	if err := Issue(cert, key, caCert, caKey, options); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cert)
	block, _ := pem.Decode(data)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject.CommonName != "alice" || len(parsed.Subject.OrganizationalUnit) != 1 || parsed.Subject.OrganizationalUnit[0] != "operator" || parsed.URIs[0].String() != "spiffe://glider/cluster/operator/alice" {
		t.Fatalf("certificate=%+v", parsed)
	}
	if err := Issue(cert, key, caCert, caKey, options); err == nil {
		t.Fatal("overwrote existing identity")
	}
	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o", info.Mode().Perm())
	}
}
