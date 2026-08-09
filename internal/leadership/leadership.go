// Package leadership runs mutating control loops under an etcd election.
package leadership

import (
	"context"
	"errors"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

type RunFunc func(context.Context) error

// Run campaigns until ctx ends. The callback is canceled immediately when the
// lease is lost; callers must make every controller operation independently
// CAS-safe because in-flight RPCs can straddle a lease-loss boundary.
func Run(ctx context.Context, client *clientv3.Client, electionPrefix, identity string, run RunFunc) error {
	if client == nil || electionPrefix == "" || identity == "" || run == nil {
		return errors.New("leadership requires client, prefix, identity, and callback")
	}
	for ctx.Err() == nil {
		session, err := concurrency.NewSession(client, concurrency.WithTTL(10), concurrency.WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("create leadership session: %w", err)
		}
		election := concurrency.NewElection(session, electionPrefix)
		if err := election.Campaign(ctx, identity); err != nil {
			session.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("campaign for leadership: %w", err)
		}
		leaderCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- run(leaderCtx) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			resign(election)
			session.Close()
			return ctx.Err()
		case <-session.Done():
			cancel()
			<-done
			session.Close()
			// Re-establish a session and campaign again if the client recovered.
		case err := <-done:
			cancel()
			resign(election)
			session.Close()
			if err == nil {
				return errors.New("leader callback returned before leadership ended")
			}
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return ctx.Err()
			}
			return err
		}
	}
	return ctx.Err()
}

func resign(election *concurrency.Election) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = election.Resign(ctx)
}
