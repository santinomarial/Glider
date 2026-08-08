// Package workload implements level-triggered replica and rolling-update convergence.
package workload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/santinomarial/glider/internal/api"
	storeapi "github.com/santinomarial/glider/internal/store"
)

type Store interface {
	ListWorkloads(context.Context) ([]api.Workload, error)
	PutWorkload(context.Context, api.Workload, int64) (api.Workload, error)
	ListTasks(context.Context) ([]api.Task, error)
	PutTask(context.Context, api.Task, int64) (api.Task, error)
	DeleteTask(context.Context, string, int64) error
}
type Controller struct {
	store Store
	now   func() time.Time
}

func New(store Store) (*Controller, error) {
	if store == nil {
		return nil, errors.New("workload store is required")
	}
	return &Controller{store: store, now: time.Now}, nil
}

func templateRevision(spec api.TaskSpec) (string, error) {
	copy := spec
	copy.WorkloadID = ""
	copy.TemplateRevision = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}
func strategy(s api.RolloutStrategy) (surge, unavailable int, deadline time.Duration, errorValue error) {
	surge, unavailable = s.MaxSurge, s.MaxUnavailable
	if surge < 0 || unavailable < 0 {
		return 0, 0, 0, errors.New("rollout budgets cannot be negative")
	}
	if surge == 0 && unavailable == 0 {
		surge = 1
	}
	deadline = s.ProgressDeadline
	if deadline <= 0 {
		deadline = 10 * time.Minute
	}
	return
}

func (c *Controller) Reconcile(ctx context.Context, w api.Workload) error {
	if w.Spec.Replicas < 0 || w.Spec.Replicas > 10000 {
		return errors.New("replicas must be between 0 and 10000")
	}
	surge, maxUnavailable, deadline, err := strategy(w.Spec.Rollout)
	if err != nil {
		return err
	}
	revision, err := templateRevision(w.Spec.Template)
	if err != nil {
		return err
	}
	tasks, err := c.store.ListTasks(ctx)
	if err != nil {
		return err
	}
	var owned []api.Task
	for _, task := range tasks {
		if task.Spec.WorkloadID == w.Metadata.ID && task.Status.Phase != api.TaskTerminated {
			owned = append(owned, task)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Metadata.ID < owned[j].Metadata.ID })
	var updated, old []api.Task
	readyTotal, updatedReady := 0, 0
	for _, task := range owned {
		if task.Status.Ready {
			readyTotal++
		}
		if task.Spec.TemplateRevision == revision {
			updated = append(updated, task)
			if task.Status.Ready {
				updatedReady++
			}
		} else {
			old = append(old, task)
		}
	}
	now := c.now().UTC()
	progressed := false
	if w.Status.UpdateRevision != revision {
		w.Status.UpdateRevision = revision
		w.Status.RolloutStartedAt = now
		w.Status.LastProgressAt = now
		w.Status.RolloutPhase = "Progressing"
		w.Status.RolloutMessage = "new template revision observed"
	}
	if len(updated) < w.Spec.Replicas && len(owned) < w.Spec.Replicas+surge {
		room := w.Spec.Replicas + surge - len(owned)
		needed := w.Spec.Replicas - len(updated)
		if room > needed {
			room = needed
		}
		used := map[string]bool{}
		for _, task := range owned {
			used[task.Metadata.ID] = true
		}
		ordinal := 0
		for created := 0; created < room; ordinal++ {
			id := fmt.Sprintf("%s-%s-%06d", w.Metadata.ID, revision[:8], ordinal)
			if used[id] {
				continue
			}
			spec := w.Spec.Template
			spec.WorkloadID = w.Metadata.ID
			spec.TemplateRevision = revision
			if _, err := c.store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: id}, Spec: spec, Status: api.TaskStatus{Phase: api.TaskPending}}, 0); err != nil && !errors.Is(err, storeapi.ErrConflict) {
				return err
			}
			used[id] = true
			created++
			progressed = true
		}
	}
	minAvailable := w.Spec.Replicas - maxUnavailable
	if minAvailable < 0 {
		minAvailable = 0
	}
	deletable := readyTotal - minAvailable
	if deletable < 0 {
		deletable = 0
	}
	// Delete old replicas only when availability remains within budget. New
	// readiness therefore gates destructive rollout progress.
	for i := len(old) - 1; i >= 0 && deletable > 0 && len(owned) > w.Spec.Replicas; i-- {
		if err := c.store.DeleteTask(ctx, old[i].Metadata.ID, old[i].Metadata.Revision); err != nil && !errors.Is(err, storeapi.ErrConflict) {
			return err
		}
		deletable--
		progressed = true
	}
	// Plain scale-down (including replicas=0) removes highest IDs while still
	// respecting availability unless no replacement is involved.
	if len(old) == 0 && len(owned) > w.Spec.Replicas {
		for i := len(owned) - 1; i >= w.Spec.Replicas; i-- {
			if err := c.store.DeleteTask(ctx, owned[i].Metadata.ID, owned[i].Metadata.Revision); err != nil && !errors.Is(err, storeapi.ErrConflict) {
				return err
			}
			progressed = true
		}
	}
	if progressed {
		w.Status.LastProgressAt = now
	}
	if len(old) > 0 && now.Sub(w.Status.LastProgressAt) > deadline {
		w.Status.RolloutPhase = "Stalled"
		w.Status.RolloutMessage = "progress deadline exceeded; healthy old replicas preserved"
	} else if len(old) == 0 && len(updated) >= w.Spec.Replicas && updatedReady >= w.Spec.Replicas-maxUnavailable {
		w.Status.RolloutPhase = "Complete"
		w.Status.RolloutMessage = "desired revision available"
		w.Status.CurrentRevision = revision
	} else {
		w.Status.RolloutPhase = "Progressing"
	}
	w.Status.ObservedGeneration = w.Metadata.Generation
	w.Status.DesiredReplicas = w.Spec.Replicas
	w.Status.CurrentReplicas = len(owned)
	w.Status.ReadyReplicas = readyTotal
	w.Status.UpdatedReplicas = len(updated)
	_, err = c.store.PutWorkload(ctx, w, w.Metadata.Revision)
	if errors.Is(err, storeapi.ErrConflict) {
		return nil
	}
	return err
}
func (c *Controller) Run(ctx context.Context, period time.Duration) error {
	if period <= 0 {
		period = 2 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		workloads, err := c.store.ListWorkloads(ctx)
		if err == nil {
			for _, w := range workloads {
				_ = c.Reconcile(ctx, w)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
