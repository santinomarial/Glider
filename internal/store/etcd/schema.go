package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	storeapi "github.com/santinomarial/glider/internal/store"
)

const CurrentSchemaVersion = 2

type SchemaState struct {
	Version       int   `json:"version"`
	MinimumReader int   `json:"minimum_reader"`
	MinimumWriter int   `json:"minimum_writer"`
	Revision      int64 `json:"-"`
}

func (s *Store) SchemaStatus(ctx context.Context) (SchemaState, error) {
	kv, err := getOne(ctx, s.client, s.key("configuration", "schema"))
	if err != nil {
		return SchemaState{}, err
	}
	var state SchemaState
	if err := json.Unmarshal(kv.Value, &state); err != nil || state.Version <= 0 || state.MinimumReader <= 0 || state.MinimumWriter <= 0 {
		return SchemaState{}, errors.New("invalid cluster schema state")
	}
	state.Revision = kv.ModRevision
	return state, nil
}

func (state SchemaState) CheckCompatibility(minimum, maximum int, write bool) error {
	if minimum <= 0 || maximum < minimum {
		return errors.New("invalid binary schema compatibility range")
	}
	required := state.MinimumReader
	mode := "read"
	if write {
		required, mode = state.MinimumWriter, "write"
	}
	if maximum < required || minimum > state.Version {
		return fmt.Errorf("binary schema range %d-%d cannot %s cluster schema %d (minimum %d)", minimum, maximum, mode, state.Version, required)
	}
	return nil
}

// EnsureSchema serializes crash-resumable migrations and verifies that this
// binary may write the resulting schema. Missing markers are treated as the
// legacy v1 layout used before migration machinery existed.
func (s *Store) EnsureSchema(ctx context.Context, limits QuotaLimits) (SchemaState, error) {
	return s.withSchemaLock(ctx, func(ctx context.Context) (SchemaState, error) {
		state, err := s.SchemaStatus(ctx)
		if errors.Is(err, storeapi.ErrNotFound) {
			initial := SchemaState{Version: 1, MinimumReader: 1, MinimumWriter: 1}
			data, _ := json.Marshal(initial)
			key := s.key("configuration", "schema")
			resp, txnErr := s.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).Then(clientv3.OpPut(key, string(data))).Commit()
			if txnErr != nil {
				return SchemaState{}, txnErr
			}
			if !resp.Succeeded {
				return SchemaState{}, storeapi.ErrConflict
			}
			state, err = s.SchemaStatus(ctx)
			if err != nil {
				return SchemaState{}, fmt.Errorf("read initialized schema: %w", err)
			}
		}
		if err != nil {
			return SchemaState{}, fmt.Errorf("read schema: %w", err)
		}
		if state.Version > CurrentSchemaVersion {
			return SchemaState{}, fmt.Errorf("cluster schema %d is newer than binary schema %d", state.Version, CurrentSchemaVersion)
		}
		for state.Version < CurrentSchemaVersion {
			switch state.Version {
			case 1:
				if err := s.ConfigureQuota(ctx, limits); err != nil {
					return SchemaState{}, fmt.Errorf("migrate schema 1 to 2: %w", err)
				}
				next := SchemaState{Version: 2, MinimumReader: 1, MinimumWriter: 2}
				if err := s.replaceSchema(ctx, state.Revision, next); err != nil {
					return SchemaState{}, err
				}
				state, err = s.SchemaStatus(ctx)
				if err != nil {
					return SchemaState{}, fmt.Errorf("read migrated schema: %w", err)
				}
			default:
				return SchemaState{}, fmt.Errorf("no migration from schema %d", state.Version)
			}
		}
		if err := state.CheckCompatibility(CurrentSchemaVersion, CurrentSchemaVersion, true); err != nil {
			return SchemaState{}, err
		}
		return state, nil
	})
}

// DowngradeSchema performs the supported backward migration for rollback.
// Operators must quiesce API writers before invoking it.
func (s *Store) DowngradeSchema(ctx context.Context, target int) (SchemaState, error) {
	return s.withSchemaLock(ctx, func(ctx context.Context) (SchemaState, error) {
		state, err := s.SchemaStatus(ctx)
		if err != nil {
			return SchemaState{}, err
		}
		if target != 1 || state.Version != 2 {
			return SchemaState{}, fmt.Errorf("unsupported schema downgrade %d to %d", state.Version, target)
		}
		tasks, err := s.ListTasks(ctx)
		if err != nil {
			return SchemaState{}, err
		}
		for _, task := range tasks {
			if len(task.Spec.Secrets) > 0 {
				return SchemaState{}, errors.New("schema v1 downgrade is unsafe while tasks reference secrets")
			}
		}
		assignments, err := s.ListAssignments(ctx)
		if err != nil {
			return SchemaState{}, err
		}
		for _, assignment := range assignments {
			if len(assignment.Secrets) > 0 {
				return SchemaState{}, errors.New("schema v1 downgrade is unsafe while assignments reference secrets")
			}
		}
		next := SchemaState{Version: 1, MinimumReader: 1, MinimumWriter: 1}
		data, _ := json.Marshal(next)
		schemaKey := s.key("configuration", "schema")
		quotaKey := s.key("configuration", "quota")
		resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(schemaKey), "=", state.Revision)).Then(clientv3.OpDelete(quotaKey), clientv3.OpPut(schemaKey, string(data))).Commit()
		if err != nil {
			return SchemaState{}, err
		}
		if !resp.Succeeded {
			return SchemaState{}, storeapi.ErrConflict
		}
		return s.SchemaStatus(ctx)
	})
}

func (s *Store) replaceSchema(ctx context.Context, revision int64, next SchemaState) error {
	data, _ := json.Marshal(next)
	key := s.key("configuration", "schema")
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", revision)).Then(clientv3.OpPut(key, string(data))).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return storeapi.ErrConflict
	}
	return nil
}

func (s *Store) withSchemaLock(ctx context.Context, fn func(context.Context) (SchemaState, error)) (SchemaState, error) {
	session, err := concurrency.NewSession(s.client, concurrency.WithTTL(30), concurrency.WithContext(ctx))
	if err != nil {
		return SchemaState{}, fmt.Errorf("create schema lock session: %w", err)
	}
	defer session.Close()
	mutex := concurrency.NewMutex(session, s.key("locks", "schema"))
	if err := mutex.Lock(ctx); err != nil {
		return SchemaState{}, fmt.Errorf("acquire schema lock: %w", err)
	}
	defer mutex.Unlock(context.Background())
	return fn(ctx)
}
