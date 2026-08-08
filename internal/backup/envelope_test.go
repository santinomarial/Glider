package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvelopeRoundTripAndTamperDetection(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 64)
	path := filepath.Join(t.TempDir(), "backup")
	file, _ := os.Create(path)
	if err := Encrypt(file, bytes.NewBufferString("snapshot"), key); err != nil {
		t.Fatal(err)
	}
	file.Close()
	source, _ := os.Open(path)
	var out bytes.Buffer
	if err := Decrypt(&out, source, key); err != nil {
		t.Fatal(err)
	}
	source.Close()
	if out.String() != "snapshot" {
		t.Fatalf("out=%q", out.String())
	}
	data, _ := os.ReadFile(path)
	data[len(data)/2] ^= 1
	_ = os.WriteFile(path, data, 0o600)
	source, _ = os.Open(path)
	defer source.Close()
	if err := Decrypt(io.Discard, source, key); err == nil {
		t.Fatal("tampered backup accepted")
	}
}
