package agent

import (
	"context"
	"time"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/health"
)

type ProbeChecker interface {
	CheckProbe(context.Context, api.Assignment, api.Probe) error
	EndpointAddress(api.Assignment) (string, error)
}
type HealthStore interface {
	Snapshot(context.Context, string) ([]api.Assignment, error)
	GetTask(context.Context, string) (api.Task, error)
	ReportTaskHealth(context.Context, string, int64, bool) error
	ReportTaskEndpoint(context.Context, string, int64, string) error
	RestartTask(context.Context, string, int64) error
}
type probeState struct {
	Health        health.State
	NextStartup   time.Time
	NextLiveness  time.Time
	NextReadiness time.Time
}
type HealthDaemon struct {
	nodeID  string
	store   HealthStore
	checker ProbeChecker
	states  map[string]probeState
	period  time.Duration
	now     func() time.Time
}

func NewHealthDaemon(nodeID string, store HealthStore, checker ProbeChecker, period time.Duration) *HealthDaemon {
	if period <= 0 {
		period = time.Second
	}
	return &HealthDaemon{nodeID: nodeID, store: store, checker: checker, states: map[string]probeState{}, period: period, now: time.Now}
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
	now := d.now()
	present := map[string]bool{}
	for _, a := range assignments {
		key := containerID(a)
		present[key] = true
		task, err := d.store.GetTask(ctx, a.TaskID)
		if err != nil || task.Status.AssignmentGeneration != a.Generation || task.Status.Phase != api.TaskRunning || task.Status.StartedAt.IsZero() {
			continue
		}
		if address, err := d.checker.EndpointAddress(a); err == nil {
			_ = d.store.ReportTaskEndpoint(ctx, a.TaskID, a.Generation, address)
		}
		state, ok := d.states[key]
		if !ok {
			state = probeState{
				NextStartup:   initialProbeAt(task.Status.StartedAt, a.Health.Startup),
				NextLiveness:  initialProbeAt(task.Status.StartedAt, a.Health.Liveness),
				NextReadiness: initialProbeAt(task.Status.StartedAt, a.Health.Readiness),
			}
		}
		if a.Health.Startup != nil && !state.Health.StartupComplete {
			if now.Before(state.NextStartup) {
				d.states[key] = state
				continue
			}
			result := health.Evaluate(state.Health, "startup", d.check(ctx, a, *a.Health.Startup), *a.Health.Startup)
			state.Health = result.State
			state.NextStartup = now.Add(probePeriod(*a.Health.Startup))
			d.states[key] = state
			if result.Restart && health.ShouldRestart(a.RestartPolicy, 1, true) {
				_ = d.store.RestartTask(ctx, a.TaskID, a.Generation)
			}
			continue
		}
		state.Health.StartupComplete = true
		if a.Health.Liveness != nil && !now.Before(state.NextLiveness) {
			result := health.Evaluate(state.Health, "liveness", d.check(ctx, a, *a.Health.Liveness), *a.Health.Liveness)
			state.Health = result.State
			state.NextLiveness = now.Add(probePeriod(*a.Health.Liveness))
			if result.Restart && health.ShouldRestart(a.RestartPolicy, 1, true) {
				d.states[key] = state
				_ = d.store.RestartTask(ctx, a.TaskID, a.Generation)
				continue
			}
		}
		if a.Health.Readiness != nil && !now.Before(state.NextReadiness) {
			result := health.Evaluate(state.Health, "readiness", d.check(ctx, a, *a.Health.Readiness), *a.Health.Readiness)
			state.Health = result.State
			state.NextReadiness = now.Add(probePeriod(*a.Health.Readiness))
			if result.ReadinessChanged {
				_ = d.store.ReportTaskHealth(ctx, a.TaskID, a.Generation, state.Health.Ready)
			}
		} else if a.Health.Readiness == nil && !state.Health.Ready {
			state.Health.Ready = true
			_ = d.store.ReportTaskHealth(ctx, a.TaskID, a.Generation, true)
		}
		d.states[key] = state
	}
	for key := range d.states {
		if !present[key] {
			delete(d.states, key)
		}
	}
}
func (d *HealthDaemon) check(ctx context.Context, a api.Assignment, p api.Probe) bool {
	return d.checker.CheckProbe(ctx, a, p) == nil
}
func probePeriod(p api.Probe) time.Duration {
	if p.Period <= 0 {
		return 10 * time.Second
	}
	return p.Period
}

func initialProbeAt(startedAt time.Time, probe *api.Probe) time.Time {
	if probe == nil || probe.InitialDelay <= 0 {
		return startedAt
	}
	return startedAt.Add(probe.InitialDelay)
}
