package agent

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"sync"
	"testing"
)

type fakeDriver struct {
	mu               sync.Mutex
	running          map[string]int64
	ensures, removes int
}

func (f *fakeDriver) Ensure(_ context.Context, a api.Assignment) (Observed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	f.running[a.TaskID] = a.Generation
	return Observed{Phase: ObservedRunning, ContainerID: a.TaskID}, nil
}
func (f *fakeDriver) Remove(_ context.Context, a api.Assignment, _ Observed) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	delete(f.running, a.TaskID)
	return nil
}
func (f *fakeDriver) Observe(_ context.Context, a api.Assignment, o Observed) (Observed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[a.TaskID] == a.Generation {
		return Observed{Phase: ObservedRunning, ContainerID: o.ContainerID}, nil
	}
	return Observed{Phase: ObservedAbsent}, nil
}
func assignment(task string, gen int64) api.Assignment {
	return api.Assignment{TaskID: task, Generation: gen, NodeID: "node"}
}
func TestReconcileIdempotentAndRemovesUndesired(t *testing.T) {
	d := &fakeDriver{running: map[string]int64{}}
	r, _ := New(t.TempDir(), d)
	ctx := context.Background()
	if err := r.Reconcile(ctx, []api.Assignment{assignment("a", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, []api.Assignment{assignment("a", 1)}); err != nil {
		t.Fatal(err)
	}
	if d.ensures != 1 {
		t.Fatalf("ensures=%d", d.ensures)
	}
	if err := r.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if d.removes != 1 {
		t.Fatalf("removes=%d", d.removes)
	}
}
func TestNewGenerationStopsOldBeforeEnsure(t *testing.T) {
	d := &fakeDriver{running: map[string]int64{}}
	r, _ := New(t.TempDir(), d)
	_ = r.Reconcile(context.Background(), []api.Assignment{assignment("a", 1)})
	_ = r.Reconcile(context.Background(), []api.Assignment{assignment("a", 2)})
	if d.removes != 1 || d.ensures != 2 || d.running["a"] != 2 {
		t.Fatalf("remove=%d ensure=%d running=%v", d.removes, d.ensures, d.running)
	}
}
func TestRestartReconstructsWithoutDuplicate(t *testing.T) {
	root := t.TempDir()
	d := &fakeDriver{running: map[string]int64{}}
	r, _ := New(root, d)
	_ = r.Reconcile(context.Background(), []api.Assignment{assignment("a", 1)})
	restarted, _ := New(root, d)
	_ = restarted.Reconcile(context.Background(), []api.Assignment{assignment("a", 1)})
	if d.ensures != 1 {
		t.Fatalf("restart duplicated ensure: %d", d.ensures)
	}
}
func TestStaleDesiredGenerationRejected(t *testing.T) {
	d := &fakeDriver{running: map[string]int64{}}
	r, _ := New(t.TempDir(), d)
	_ = r.Reconcile(context.Background(), []api.Assignment{assignment("a", 2)})
	_ = r.Reconcile(context.Background(), []api.Assignment{assignment("a", 1)})
	if d.running["a"] != 2 || d.ensures != 1 {
		t.Fatalf("stale generation acted: %+v ensures=%d", d.running, d.ensures)
	}
}
