package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryContract(t *testing.T) {
	if err := validate(filepath.Join("..", "..", "api", "proto", "glider", "v2")); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsUntypedAndMalformedContract(t *testing.T) {
	dir := t.TempDir()
	schema := `syntax = "proto3";
package glider.v2;
service Broken {
  rpc Mutate(google.protobuf.Struct) returns (google.protobuf.Struct);
}
message Duplicate {
  string first = 1;
  string second = 1;
}
enum State {
  STATE_READY = 1;
}
`
	if err := os.WriteFile(filepath.Join(dir, "broken.proto"), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	err := validate(dir)
	if err == nil {
		t.Fatal("malformed contract unexpectedly passed")
	}
	for _, expected := range []string{"RPC request must be named MutateRequest", "RPC boundaries must use Glider-owned messages", "field number 1 duplicates", "first enum value must be zero"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error %q does not contain %q", err, expected)
		}
	}
}
