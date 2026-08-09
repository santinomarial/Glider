package process

import (
	"slices"
	"testing"
)

func TestSecretEnvironmentIsExcludedFromDurableState(t *testing.T) {
	cfg := Config{Env: []string{"IMAGE=value"}, SecretEnv: []string{"PASSWORD=secret"}}
	got := durableEnvironment(cfg)
	if !slices.Equal(got, cfg.Env) || slices.Contains(got, "PASSWORD=secret") {
		t.Fatalf("durable environment = %q", got)
	}
}
