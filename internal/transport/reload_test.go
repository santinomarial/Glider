package transport

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santinomarial/glider/internal/pki"
)

func TestCertificateReloadKeepsLastPairAcrossPartialRotation(t *testing.T) {
	directory := t.TempDir()
	caCert, caKey := filepath.Join(directory, "ca.crt"), filepath.Join(directory, "ca.key")
	if err := pki.InitCA(caCert, caKey, "cluster"); err != nil {
		t.Fatal(err)
	}
	liveCert, liveKey := filepath.Join(directory, "live.crt"), filepath.Join(directory, "live.key")
	options := pki.IssueOptions{Name: "node-a", Role: "node", ClusterID: "cluster", Client: true, ValidFor: 24 * time.Hour}
	if err := pki.Issue(liveCert, liveKey, caCert, caKey, options); err != nil {
		t.Fatal(err)
	}
	reloader, err := newCertificateReloader(liveCert, liveKey)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := reloader.Certificate()

	nextCert, nextKey := filepath.Join(directory, "next.crt"), filepath.Join(directory, "next.key")
	if err = pki.Issue(nextCert, nextKey, caCert, caKey, options); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(nextCert, liveCert); err != nil {
		t.Fatal(err)
	}
	during, err := reloader.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if during.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatal("partially rotated pair replaced last valid identity")
	}
	if err = os.Rename(nextKey, liveKey); err != nil {
		t.Fatal(err)
	}
	after, err := reloader.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if after.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Fatal("complete renewed pair was not loaded")
	}
}
