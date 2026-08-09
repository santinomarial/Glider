package agent

import (
	"encoding/base64"
	"testing"

	"github.com/santinomarial/glider/internal/api"
)

func TestDecodeSecretValuesRequiresExactRequestedEnvironment(t *testing.T) {
	assignment := api.Assignment{Secrets: []api.SecretEnvRef{{SecretID: "database", Key: "password", Env: "PASSWORD"}}}
	values, err := decodeSecretValues(map[string]any{"PASSWORD": base64.StdEncoding.EncodeToString([]byte("value"))}, assignment)
	if err != nil || len(values) != 1 || values[0] != "PASSWORD=value" {
		t.Fatalf("values = %q, %v", values, err)
	}
	if _, err := decodeSecretValues(map[string]any{"OTHER": base64.StdEncoding.EncodeToString([]byte("value"))}, assignment); err == nil {
		t.Fatal("unrequested environment accepted")
	}
	if _, err := decodeSecretValues(map[string]any{"PASSWORD": base64.StdEncoding.EncodeToString([]byte{'a', 0, 'b'})}, assignment); err == nil {
		t.Fatal("NUL-containing environment value accepted")
	}
}
