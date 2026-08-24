package agent

import (
	"context"
	"errors"
	"github.com/santinomarial/glider/internal/api"
	"testing"
	"time"
)

type healthFakeStore struct {
	assignments []api.Assignment
	tasks       map[string]api.Task
	reported    []bool
	restarts    int
	addresses   []string
}

func (s *healthFakeStore) Snapshot(context.Context, string) ([]api.Assignment, error) {
	return s.assignments, nil
}
func (s *healthFakeStore) GetTask(_ context.Context, taskID string) (api.Task, error) {
	return s.tasks[taskID], nil
}
func (s *healthFakeStore) ReportTaskHealth(_ context.Context, _ string, _ int64, ready bool) error {
	s.reported = append(s.reported, ready)
	return nil
}
func (s *healthFakeStore) ReportTaskEndpoint(_ context.Context, _ string, _ int64, address string) error {
	s.addresses = append(s.addresses, address)
	return nil
}
func (s *healthFakeStore) RestartTask(context.Context, string, int64) error { s.restarts++; return nil }

type healthFakeChecker struct {
	fail  bool
	calls int
	kinds []api.ProbeKind
}

func (c *healthFakeChecker) EndpointAddress(api.Assignment) (string, error) {
	return "10.64.0.2", nil
}

func (c *healthFakeChecker) CheckProbe(_ context.Context, _ api.Assignment, probe api.Probe) error {
	c.calls++
	c.kinds = append(c.kinds, probe.Kind)
	if c.fail {
		return errors.New("unhealthy")
	}
	return nil
}

func TestHealthDaemonRunsProbePeriodsIndependently(t *testing.T) {
	started := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	liveness := api.Probe{Kind: api.ProbeHTTP, Period: 10 * time.Second}
	readiness := api.Probe{Kind: api.ProbeTCP, Period: 3 * time.Second}
	a := api.Assignment{TaskID: "task", Generation: 2, Health: api.HealthSpec{Liveness: &liveness, Readiness: &readiness}}
	store := &healthFakeStore{assignments: []api.Assignment{a}, tasks: map[string]api.Task{"task": {Status: api.TaskStatus{Phase: api.TaskRunning, AssignmentGeneration: 2, StartedAt: started}}}}
	checker := &healthFakeChecker{}
	daemon := NewHealthDaemon("node", store, checker, time.Second)
	now := started
	daemon.now = func() time.Time { return now }
	daemon.reconcile(context.Background(), store.assignments)
	if checker.calls != 2 {
		t.Fatalf("initial probes=%d", checker.calls)
	}
	now = started.Add(4 * time.Second)
	daemon.reconcile(context.Background(), store.assignments)
	if checker.calls != 3 || checker.kinds[2] != api.ProbeTCP {
		t.Fatalf("period scheduling: calls=%d kinds=%v", checker.calls, checker.kinds)
	}
}
func TestHealthDaemonReportsReadinessAndRequestsDurableRestart(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	readiness := api.Probe{Kind: api.ProbeTCP, SuccessThreshold: 1}
	a := api.Assignment{TaskID: "task", Generation: 1, RestartPolicy: api.RestartAlways, Health: api.HealthSpec{Readiness: &readiness}}
	store := &healthFakeStore{assignments: []api.Assignment{a}, tasks: map[string]api.Task{"task": {Status: api.TaskStatus{Phase: api.TaskRunning, AssignmentGeneration: 1, StartedAt: now}}}}
	checker := &healthFakeChecker{}
	daemon := NewHealthDaemon("node", store, checker, time.Millisecond)
	daemon.now = func() time.Time { return now }
	daemon.reconcile(context.Background(), store.assignments)
	if len(store.reported) != 1 || !store.reported[0] {
		t.Fatalf("readiness=%v", store.reported)
	}
	liveness := api.Probe{Kind: api.ProbeTCP, FailureThreshold: 1}
	store.assignments[0].Health = api.HealthSpec{Liveness: &liveness}
	checker.fail = true
	daemon.reconcile(context.Background(), store.assignments)
	if store.restarts != 1 {
		t.Fatalf("restarts=%d", store.restarts)
	}
}

func TestHealthDaemonHonorsInitialDelayFromDurableStartTime(t *testing.T) {
	started := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	readiness := api.Probe{Kind: api.ProbeTCP, InitialDelay: 30 * time.Second, Period: 5 * time.Second, SuccessThreshold: 1}
	a := api.Assignment{TaskID: "task", Generation: 4, Health: api.HealthSpec{Readiness: &readiness}}
	store := &healthFakeStore{assignments: []api.Assignment{a}, tasks: map[string]api.Task{"task": {Status: api.TaskStatus{Phase: api.TaskRunning, AssignmentGeneration: 4, StartedAt: started}}}}
	checker := &healthFakeChecker{}
	daemon := NewHealthDaemon("node", store, checker, time.Second)
	now := started.Add(29 * time.Second)
	daemon.now = func() time.Time { return now }
	daemon.reconcile(context.Background(), store.assignments)
	if checker.calls != 0 || len(store.reported) != 0 {
		t.Fatalf("probe ran before initial delay: calls=%d readiness=%v", checker.calls, store.reported)
	}
	now = started.Add(30 * time.Second)
	daemon.reconcile(context.Background(), store.assignments)
	if checker.calls != 1 || len(store.reported) != 1 || !store.reported[0] {
		t.Fatalf("probe did not run at deadline: calls=%d readiness=%v", checker.calls, store.reported)
	}
}
