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
	terminal         map[string]Observed
	ensures, removes int
}

func (f *fakeDriver) Ensure(_ context.Context, a api.Assignment) (Observed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	f.running[a.TaskID] = a.Generation
	delete(f.terminal, a.TaskID)
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
	if observed, ok := f.terminal[a.TaskID]; ok {
		return observed, nil
	}
	if f.running[a.TaskID] == a.Generation {
		return Observed{Phase: ObservedRunning, ContainerID: o.ContainerID}, nil
	}
	return Observed{Phase: ObservedAbsent}, nil
}

type fakeStatusReporter struct {
	running, completed, restarts int
	exitCode                     *int
	reason                       string
}

func (f *fakeStatusReporter) ReportTaskRunning(context.Context, string, int64) error {
	f.running++
	return nil
}
func (f *fakeStatusReporter) CompleteTask(_ context.Context, _ string, _ int64, exitCode *int, reason string) error {
	f.completed++
	f.exitCode = exitCode
	f.reason = reason
	return nil
}
func (f *fakeStatusReporter) RestartTask(context.Context, string, int64) error {
	f.restarts++
	return nil
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

func TestReconcileReportsRunningAndCompletesNeverPolicy(t *testing.T) {
	d := &fakeDriver{running: map[string]int64{}, terminal: map[string]Observed{}}
	status := &fakeStatusReporter{}
	r, err := New(t.TempDir(), d, status)
	if err != nil {
		t.Fatal(err)
	}
	a := assignment("a", 1)
	a.RestartPolicy = api.RestartNever
	if err := r.Reconcile(context.Background(), []api.Assignment{a}); err != nil {
		t.Fatal(err)
	}
	if status.running != 1 {
		t.Fatalf("running reports=%d", status.running)
	}
	code := 0
	d.mu.Lock()
	delete(d.running, "a")
	d.terminal["a"] = Observed{Phase: ObservedExited, ExitCode: &code}
	d.mu.Unlock()
	if err := r.Reconcile(context.Background(), []api.Assignment{a}); err != nil {
		t.Fatal(err)
	}
	if status.completed != 1 || status.restarts != 0 || status.exitCode == nil || *status.exitCode != 0 {
		t.Fatalf("completed=%d restarts=%d exit=%v", status.completed, status.restarts, status.exitCode)
	}
	if d.ensures != 1 {
		t.Fatalf("terminal task relaunched locally: ensures=%d", d.ensures)
	}
}

func TestReconcileAppliesExitAwareRestartPolicy(t *testing.T) {
	tests := []struct {
		name        string
		policy      api.RestartPolicy
		exitCode    int
		restarts    int
		completions int
	}{
		{name: "on-failure-success", policy: api.RestartOnFailure, exitCode: 0, completions: 1},
		{name: "on-failure-error", policy: api.RestartOnFailure, exitCode: 17, restarts: 1},
		{name: "always-success", policy: api.RestartAlways, exitCode: 0, restarts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDriver{running: map[string]int64{}, terminal: map[string]Observed{}}
			status := &fakeStatusReporter{}
			r, err := New(t.TempDir(), d, status)
			if err != nil {
				t.Fatal(err)
			}
			a := assignment("a", 1)
			a.RestartPolicy = tt.policy
			if err := r.Reconcile(context.Background(), []api.Assignment{a}); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			delete(d.running, "a")
			d.terminal["a"] = Observed{Phase: ObservedExited, ExitCode: &tt.exitCode}
			d.mu.Unlock()
			if err := r.Reconcile(context.Background(), []api.Assignment{a}); err != nil {
				t.Fatal(err)
			}
			if status.restarts != tt.restarts || status.completed != tt.completions {
				t.Fatalf("restarts=%d completions=%d", status.restarts, status.completed)
			}
		})
	}
}
