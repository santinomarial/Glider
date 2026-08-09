package configcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santinomarial/glider/internal/pki"
)

func TestValidateBackupConfigAndReferencedFiles(t *testing.T) {
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	caCert, caKey := filepath.Join(pkiDir, "ca.crt"), filepath.Join(pkiDir, "ca.key")
	if err := pki.InitCA(caCert, caKey, "cluster"); err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(pkiDir, "client.crt"), filepath.Join(pkiDir, "client.key")
	if err := pki.Issue(cert, key, caCert, caKey, pki.IssueOptions{Name: "backup", Role: "etcd-client", ClusterID: "cluster", Client: true, ValidFor: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backup.key"), make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `GLIDER_BACKUP_ARGS="--endpoint=https://etcd:2379 --key-file=/backup.key --tls-cert=/pki/client.crt --tls-key=/pki/client.key --ca=/pki/ca.crt --tls-server-name=etcd.internal --timeout=10m"
GLIDER_BACKUP_KEY_FILE="/backup.key"
GLIDER_BACKUP_RETENTION_DAYS="30"
`
	path := filepath.Join(root, "backup.env")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Validate(Options{Kind: "backup", File: path, Root: root, CheckFiles: true})
	if err != nil || !result.Valid || result.Files != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidateRejectsPlaceholderDuplicateAndShellSyntax(t *testing.T) {
	for name, config := range map[string]string{
		"placeholder": `GLIDERD_ARGS="--node-id=CHANGE_ME"`,
		"duplicate":   "GLIDERD_ARGS=x\nGLIDERD_ARGS=y\n",
		"shell":       `GLIDERD_ARGS="--node-id=$(touch /tmp/owned)"`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node.env")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(Options{Kind: "node", File: path}); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestParseArgumentsRejectsAmbiguousForms(t *testing.T) {
	for _, value := range []string{"--flag value", "--flag=", "--flag=a --flag=b", "--flag=a;evil"} {
		if _, err := parseArguments(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	arguments, err := parseArguments(strings.Join([]string{"--node-id=node-a", "--cluster-id=prod"}, " "))
	if err != nil || arguments["node-id"] != "node-a" {
		t.Fatalf("arguments=%v err=%v", arguments, err)
	}
}
