// Package workload implements level-triggered replica convergence.
package workload

import (
	"context"
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
type Controller struct{ store Store }

func New(store Store) (*Controller, error) {
	if store == nil {
		return nil, errors.New("workload store is required")
	}
	return &Controller{store: store}, nil
}
func (c *Controller) Reconcile(ctx context.Context, w api.Workload) error {
	if w.Spec.Replicas < 0 || w.Spec.Replicas > 10000 {
		return errors.New("replicas must be between 0 and 10000")
	}
	tasks, err := c.store.ListTasks(ctx)
	if err != nil {
		return err
	}
	var owned []api.Task
	for _, task := range tasks {
		if task.Spec.WorkloadID == w.Metadata.ID {
			owned = append(owned, task)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Metadata.ID < owned[j].Metadata.ID })
	for ordinal := len(owned); ordinal < w.Spec.Replicas; ordinal++ {
		spec := w.Spec.Template
		spec.WorkloadID = w.Metadata.ID
		id := fmt.Sprintf("%s-%06d", w.Metadata.ID, ordinal)
		_, err := c.store.PutTask(ctx, api.Task{Metadata: api.Metadata{ID: id}, Spec: spec, Status: api.TaskStatus{Phase: api.TaskPending}}, 0)
		if err != nil && !errors.Is(err, storeapi.ErrConflict) {
			return err
		}
	}
	for i := len(owned) - 1; i >= w.Spec.Replicas; i-- {
		if err := c.store.DeleteTask(ctx, owned[i].Metadata.ID, owned[i].Metadata.Revision); err != nil && !errors.Is(err, storeapi.ErrConflict) {
			return err
		}
	}
	current, ready := 0, 0
	for _, task := range owned {
		if task.Status.Phase != api.TaskTerminated {
			current++
		}
		if task.Status.Ready {
			ready++
		}
	}
	w.Status = api.WorkloadStatus{ObservedGeneration: w.Metadata.Generation, DesiredReplicas: w.Spec.Replicas, CurrentReplicas: current, ReadyReplicas: ready}
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
