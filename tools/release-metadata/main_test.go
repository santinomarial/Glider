package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataVerificationRejectsArtifactTampering(t *testing.T) {
	directory := t.TempDir()
	artifact := filepath.Join(directory, "glider-test.tar.gz")
	if err := os.WriteFile(artifact, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"
	if err := sbom([]string{"--output", filepath.Join(directory, "sbom.spdx.json"), "--version", "v1.0.0", "--commit", commit, "--epoch", "1700000000"}); err != nil {
		t.Fatal(err)
	}
	if err := provenance([]string{"--output", filepath.Join(directory, "provenance.intoto.json"), "--version", "v1.0.0", "--commit", commit, "--epoch", "1700000000", artifact}); err != nil {
		t.Fatal(err)
	}
	if err := verify([]string{"--dir", directory}); err != nil {
		t.Fatalf("verify valid metadata: %v", err)
	}
	if err := os.WriteFile(artifact, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verify([]string{"--dir", directory}); err == nil {
		t.Fatal("tampered artifact matched provenance")
	}
}
