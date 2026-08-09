package etcd

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/santinomarial/glider/internal/api"
)

func TestConcurrentSchemaMigrationAndRollbackRoundTrip(t *testing.T) {
	client := startEtcd(t)
	store, _ := New(client, "schema-cluster")
	ctx := context.Background()
	if _, err := store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "existing"}, Spec: api.TaskSpec{Image: "image", Resources: api.Resources{CPUMilli: 100, MemoryBytes: 100}}}, 0); err != nil {
		t.Fatal(err)
	}
	limits := QuotaLimits{Tasks: 10, Workloads: 10, Services: 10, Resources: api.Resources{CPUMilli: 1000, MemoryBytes: 1000}}
	const replicas = 8
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.EnsureSchema(ctx, limits)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
	state, err := store.SchemaStatus(ctx)
	if err != nil || state.Version != 2 || state.MinimumWriter != 2 {
		t.Fatalf("schema = %+v, %v", state, err)
	}
	quota, _, err := store.quota(ctx)
	if err != nil || quota.Usage.Tasks != 1 || quota.Usage.Resources.CPUMilli != 100 {
		t.Fatalf("quota = %+v, %v", quota, err)
	}
	rolledBack, err := store.DowngradeSchema(ctx, 1)
	if err != nil || rolledBack.Version != 1 {
		t.Fatalf("rollback = %+v, %v", rolledBack, err)
	}
	if _, _, err := store.quota(ctx); err == nil {
		t.Fatal("v2 quota ledger remained after backward migration")
	}
	restored, err := store.EnsureSchema(ctx, limits)
	if err != nil || restored.Version != 2 {
		t.Fatalf("forward migration after rollback = %+v, %v", restored, err)
	}
}

func TestSchemaCompatibilitySeparatesReadersAndWriters(t *testing.T) {
	state := SchemaState{Version: 2, MinimumReader: 1, MinimumWriter: 2}
	if err := state.CheckCompatibility(1, 1, false); err != nil {
		t.Fatalf("v1 reader denied: %v", err)
	}
	if err := state.CheckCompatibility(1, 1, true); err == nil {
		t.Fatal("v1 writer accepted on v2 schema")
	}
	if err := state.CheckCompatibility(2, 2, true); err != nil {
		t.Fatalf("v2 writer denied: %v", err)
	}
	if err := state.CheckCompatibility(3, 3, false); err == nil {
		t.Fatal("future-only binary accepted old schema")
	}
}

func TestSchemaDowngradeRejectsUnsupportedTargets(t *testing.T) {
	client := startEtcd(t)
	store, _ := New(client, "schema-target-cluster")
	limits := QuotaLimits{Tasks: 1, Workloads: 1, Services: 1, Resources: api.Resources{CPUMilli: 1, MemoryBytes: 1}}
	if _, err := store.EnsureSchema(context.Background(), limits); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DowngradeSchema(context.Background(), 0); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("unsupported downgrade error = %v", err)
	}
}

func TestSchemaDowngradeRejectsSecretBearingTasks(t *testing.T) {
	client := startEtcd(t)
	store, _ := New(client, "schema-secret-cluster")
	ctx := context.Background()
	limits := QuotaLimits{Tasks: 2, Workloads: 1, Services: 1, Resources: api.Resources{CPUMilli: 10, MemoryBytes: 10}}
	if _, err := store.EnsureSchema(ctx, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: "secret-task"}, Spec: api.TaskSpec{Image: "image", Secrets: []api.SecretEnvRef{{SecretID: "database", Key: "password", Env: "PASSWORD"}}}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DowngradeSchema(ctx, 1); err == nil {
		t.Fatal("unsafe downgrade with secret-bearing task succeeded")
	}
	state, _ := store.SchemaStatus(ctx)
	if state.Version != 2 {
		t.Fatalf("failed downgrade changed schema to %d", state.Version)
	}
}
