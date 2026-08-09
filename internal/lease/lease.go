// Package lease implements node ownership and self-fencing over etcd leases.
package lease

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

var ErrNodeOwned = errors.New("node lease is already owned")

type Manager struct {
	client     *clientv3.Client
	key, owner string
	ttl        int64
	fenceAfter time.Duration
}

func New(client *clientv3.Client, clusterID, nodeID, owner string, ttl time.Duration, fenceAfter time.Duration) (*Manager, error) {
	if client == nil || clusterID == "" || nodeID == "" || owner == "" {
		return nil, errors.New("client, cluster, node, and owner are required")
	}
	if ttl < time.Second {
		ttl = 10 * time.Second
	}
	if fenceAfter <= ttl {
		fenceAfter = 2 * ttl
	}
	return &Manager{client: client, key: path.Join("/glider/v1/clusters", clusterID, "leases/nodes", nodeID), owner: owner, ttl: int64(ttl / time.Second), fenceAfter: fenceAfter}, nil
}

// Run owns one ephemeral key. If renewal cannot be proven before fenceAfter,
// fence is called exactly once and Run returns: uncertainty removes authority.
func (m *Manager) Run(ctx context.Context, fence func(context.Context) error) error {
	grant, err := m.client.Grant(ctx, m.ttl)
	if err != nil {
		return err
	}
	acquired, err := m.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(m.key), "=", 0)).Then(clientv3.OpPut(m.key, m.owner, clientv3.WithLease(grant.ID))).Commit()
	if err != nil {
		return err
	}
	if !acquired.Succeeded {
		revoke(m.client, grant.ID)
		return ErrNodeOwned
	}
	defer revoke(m.client, grant.ID)
	interval := time.Duration(m.ttl) * time.Second / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastSuccess := time.Now()
	fenced := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, interval)
			_, renewErr := m.client.KeepAliveOnce(renewCtx, grant.ID)
			cancel()
			if renewErr == nil {
				lastSuccess = time.Now()
				continue
			}
			if time.Since(lastSuccess) >= m.fenceAfter && !fenced {
				fenced = true
				if fenceErr := fence(context.Background()); fenceErr != nil {
					return fmt.Errorf("self-fence after lease loss: %w", fenceErr)
				}
				return fmt.Errorf("node lease renewal unproven for %s: %w", m.fenceAfter, renewErr)
			}
		}
	}
}

func revoke(client *clientv3.Client, id clientv3.LeaseID) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = client.Revoke(ctx, id)
}

func NodeAlive(ctx context.Context, client *clientv3.Client, clusterID, nodeID string) (bool, error) {
	resp, err := client.Get(ctx, path.Join("/glider/v1/clusters", clusterID, "leases/nodes", nodeID))
	return err == nil && len(resp.Kvs) == 1, err
}
