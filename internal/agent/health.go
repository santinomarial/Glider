package agent

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/health"
	"time"
)

type ProbeChecker interface {
	CheckProbe(context.Context, api.Assignment, api.Probe) error
	EndpointAddress(api.Assignment) (string, error)
}
type HealthStore interface {
	Snapshot(context.Context, string) ([]api.Assignment, error)
	ReportTaskHealth(context.Context, string, int64, bool) error
	ReportTaskEndpoint(context.Context, string, int64, string) error
	RestartTask(context.Context, string, int64) error
}
type HealthDaemon struct {
	nodeID   string
	store    HealthStore
	checker  ProbeChecker
	states   map[string]health.State
	next     map[string]time.Time
	pending  map[string]bool
	restarts map[string]int
	period   time.Duration
}

func NewHealthDaemon(nodeID string, store HealthStore, checker ProbeChecker, period time.Duration) *HealthDaemon {
	if period <= 0 {
		period = time.Second
	}
	return &HealthDaemon{nodeID: nodeID, store: store, checker: checker, states: map[string]health.State{}, next: map[string]time.Time{}, pending: map[string]bool{}, restarts: map[string]int{}, period: period}
}
func (d *HealthDaemon) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.period)
	defer ticker.Stop()
	for {
		assignments, err := d.store.Snapshot(ctx, d.nodeID)
		if err == nil {
			d.reconcile(ctx, assignments)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (d *HealthDaemon) reconcile(ctx context.Context, assignments []api.Assignment) {
	now := time.Now()
	present := map[string]bool{}
	for _, a := range assignments {
		key := containerID(a)
		present[key] = true
		if address, err := d.checker.EndpointAddress(a); err == nil {
			_ = d.store.ReportTaskEndpoint(ctx, a.TaskID, a.Generation, address)
		}
		if now.Before(d.next[key]) {
			continue
		}
		if d.pending[key] {
			_ = d.store.RestartTask(ctx, a.TaskID, a.Generation)
			delete(d.pending, key)
			continue
		}
		state := d.states[key]
		if a.Health.Startup != nil && !state.StartupComplete {
			result := health.Evaluate(state, "startup", d.check(ctx, a, *a.Health.Startup), *a.Health.Startup)
			d.states[key] = result.State
			d.next[key] = now.Add(probePeriod(*a.Health.Startup))
			if result.Restart {
				d.restart(ctx, a, key)
			}
			continue
		}
		state.StartupComplete = true
		if a.Health.Liveness != nil {
			result := health.Evaluate(state, "liveness", d.check(ctx, a, *a.Health.Liveness), *a.Health.Liveness)
			state = result.State
			if result.Restart {
				d.states[key] = state
				d.restart(ctx, a, key)
				continue
			}
		}
		if a.Health.Readiness != nil {
			result := health.Evaluate(state, "readiness", d.check(ctx, a, *a.Health.Readiness), *a.Health.Readiness)
			state = result.State
			if result.ReadinessChanged {
				_ = d.store.ReportTaskHealth(ctx, a.TaskID, a.Generation, state.Ready)
			}
		} else if !state.Ready {
			state.Ready = true
			_ = d.store.ReportTaskHealth(ctx, a.TaskID, a.Generation, true)
		}
		d.states[key] = state
		d.next[key] = now.Add(d.period)
	}
	for key := range d.states {
		if !present[key] {
			delete(d.states, key)
			delete(d.next, key)
			delete(d.pending, key)
		}
	}
}
func (d *HealthDaemon) check(ctx context.Context, a api.Assignment, p api.Probe) bool {
	return d.checker.CheckProbe(ctx, a, p) == nil
}
func (d *HealthDaemon) restart(_ context.Context, a api.Assignment, key string) {
	if !health.ShouldRestart(a.RestartPolicy, 1, true) {
		return
	}
	delay := health.RestartBackoff(d.restarts[a.TaskID], time.Second, time.Minute)
	d.restarts[a.TaskID]++
	d.next[key] = time.Now().Add(delay)
	d.pending[key] = true
}
func probePeriod(p api.Probe) time.Duration {
	if p.Period <= 0 {
		return 10 * time.Second
	}
	return p.Period
}
