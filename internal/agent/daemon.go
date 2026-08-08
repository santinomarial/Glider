package agent

import (
	"context"
	"errors"
	"time"

	"github.com/santinomarial/glider/internal/api"
)

type Source interface {
	Snapshot(context.Context, string) ([]api.Assignment, error)
	Watch(context.Context, string) (<-chan struct{}, error)
}
type Daemon struct {
	nodeID     string
	source     Source
	reconciler *Reconciler
	resync     time.Duration
}

func NewDaemon(nodeID string, source Source, r *Reconciler, resync time.Duration) (*Daemon, error) {
	if nodeID == "" || source == nil || r == nil {
		return nil, errors.New("daemon requires node ID, source, and reconciler")
	}
	if resync <= 0 {
		resync = 30 * time.Second
	}
	return &Daemon{nodeID, source, r, resync}, nil
}
func (d *Daemon) Run(ctx context.Context) error {
	events, err := d.source.Watch(ctx, d.nodeID)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(d.resync)
	defer ticker.Stop()
	for {
		desired, err := d.source.Snapshot(ctx, d.nodeID)
		if err != nil {
			return err
		}
		if err := d.reconciler.Reconcile(ctx, desired); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-events:
			if !ok {
				return errors.New("assignment watch closed")
			}
		case <-ticker.C:
		}
	}
}
