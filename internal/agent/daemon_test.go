package agent

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu        sync.Mutex
	desired   []api.Assignment
	events    chan struct{}
	snapshots int
}

func (s *fakeSource) Snapshot(context.Context, string) ([]api.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots++
	return append([]api.Assignment(nil), s.desired...), nil
}
func (s *fakeSource) Watch(context.Context, string) (<-chan struct{}, error) { return s.events, nil }
func TestDaemonEventAndPeriodicResync(t *testing.T) {
	source := &fakeSource{events: make(chan struct{}, 1)}
	driver := &fakeDriver{running: map[string]int64{}}
	r, _ := New(t.TempDir(), driver)
	d, _ := NewDaemon("node", source, r, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	source.mu.Lock()
	source.desired = []api.Assignment{assignment("a", 1)}
	source.mu.Unlock()
	source.events <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		driver.mu.Lock()
		running := driver.running["a"] == 1
		driver.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	source.mu.Lock()
	snapshots := source.snapshots
	source.mu.Unlock()
	if snapshots < 2 {
		t.Fatalf("snapshots=%d", snapshots)
	}
}
