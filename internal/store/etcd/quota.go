package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/santinomarial/glider/internal/api"
	storeapi "github.com/santinomarial/glider/internal/store"
)

type QuotaLimits struct {
	Tasks     int64         `json:"tasks"`
	Workloads int64         `json:"workloads"`
	Services  int64         `json:"services"`
	Resources api.Resources `json:"resources"`
}

type quotaUsage struct {
	Tasks     int64         `json:"tasks"`
	Workloads int64         `json:"workloads"`
	Services  int64         `json:"services"`
	Resources api.Resources `json:"resources"`
}

type quotaState struct {
	Version int         `json:"version"`
	Limits  QuotaLimits `json:"limits"`
	Usage   quotaUsage  `json:"usage"`
}

func (q QuotaLimits) validate() error {
	if q.Tasks <= 0 || q.Workloads <= 0 || q.Services <= 0 || q.Resources.CPUMilli <= 0 || q.Resources.MemoryBytes <= 0 {
		return errors.New("all cluster quota limits must be positive")
	}
	return nil
}

// ConfigureQuota creates the cluster-wide quota ledger or verifies that an
// existing control-plane replica uses exactly the same limits. Bootstrap usage
// is computed before the create-only transaction; callers must configure quota
// before accepting mutations.
func (s *Store) ConfigureQuota(ctx context.Context, limits QuotaLimits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	key := s.key("configuration", "quota")
	if kv, err := getOne(ctx, s.client, key); err == nil {
		state, err := decodeQuota(kv)
		if err != nil {
			return err
		}
		if state.Limits != limits {
			return errors.New("configured quota differs from persisted cluster quota")
		}
		return nil
	} else if !errors.Is(err, storeapi.ErrNotFound) {
		return err
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	workloads, err := s.ListWorkloads(ctx)
	if err != nil {
		return err
	}
	services, err := s.ListServices(ctx)
	if err != nil {
		return err
	}
	state := quotaState{Version: 1, Limits: limits, Usage: quotaUsage{Tasks: int64(len(tasks)), Workloads: int64(len(workloads)), Services: int64(len(services))}}
	for _, task := range tasks {
		state.Usage.Resources = state.Usage.Resources.Add(task.Spec.Resources)
	}
	if err := state.withinLimits(); err != nil {
		return fmt.Errorf("existing resources exceed configured quota: %w", err)
	}
	data, _ := json.Marshal(state)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).Then(clientv3.OpPut(key, string(data))).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return s.ConfigureQuota(ctx, limits)
	}
	return nil
}

func decodeQuota(kv *mvccpb.KeyValue) (quotaState, error) {
	var state quotaState
	if err := json.Unmarshal(kv.Value, &state); err != nil || state.Version != 1 {
		return state, errors.New("invalid cluster quota ledger")
	}
	return state, nil
}

func (q quotaState) withinLimits() error {
	if q.Usage.Tasks > q.Limits.Tasks {
		return storeapi.ErrQuotaExceeded
	}
	if q.Usage.Workloads > q.Limits.Workloads {
		return storeapi.ErrQuotaExceeded
	}
	if q.Usage.Services > q.Limits.Services {
		return storeapi.ErrQuotaExceeded
	}
	if !q.Limits.Resources.Fits(q.Usage.Resources) {
		return storeapi.ErrQuotaExceeded
	}
	if q.Usage.Tasks < 0 || q.Usage.Workloads < 0 || q.Usage.Services < 0 || q.Usage.Resources.CPUMilli < 0 || q.Usage.Resources.MemoryBytes < 0 {
		return errors.New("negative cluster quota usage")
	}
	return nil
}

func (s *Store) quota(ctx context.Context) (quotaState, *mvccpb.KeyValue, error) {
	kv, err := getOne(ctx, s.client, s.key("configuration", "quota"))
	if err != nil {
		return quotaState{}, nil, err
	}
	state, err := decodeQuota(kv)
	return state, kv, err
}

func (s *Store) putTaskWithQuota(ctx context.Context, task api.Task, expected int64) (api.Task, bool, error) {
	state, quotaKV, err := s.quota(ctx)
	if errors.Is(err, storeapi.ErrNotFound) {
		return task, false, nil
	}
	if err != nil {
		return task, true, err
	}
	key := s.key("tasks", task.Metadata.ID)
	existingKV, existingErr := getOne(ctx, s.client, key)
	var old api.Task
	cmp := clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
	if existingErr == nil {
		if expected <= 0 || existingKV.ModRevision != expected {
			return task, true, storeapi.ErrConflict
		}
		if err := json.Unmarshal(existingKV.Value, &old); err != nil {
			return task, true, err
		}
		cmp = clientv3.Compare(clientv3.ModRevision(key), "=", expected)
		if old.Spec.Resources == task.Spec.Resources {
			task.APIVersion = api.Version
			task.Metadata.Revision = 0
			data, _ := json.Marshal(task)
			resp, err := s.client.Txn(ctx).If(cmp).Then(clientv3.OpPut(key, string(data))).Commit()
			if err != nil {
				return task, true, err
			}
			if !resp.Succeeded {
				return task, true, storeapi.ErrConflict
			}
			setRevision(&task, resp.Header.Revision)
			return task, true, nil
		}
		state.Usage.Resources = state.Usage.Resources.Sub(old.Spec.Resources)
	} else if !errors.Is(existingErr, storeapi.ErrNotFound) {
		return task, true, existingErr
	} else {
		if expected > 0 {
			return task, true, storeapi.ErrConflict
		}
		state.Usage.Tasks++
	}
	state.Usage.Resources = state.Usage.Resources.Add(task.Spec.Resources)
	if err := state.withinLimits(); err != nil {
		return task, true, err
	}
	task.APIVersion = api.Version
	task.Metadata.Revision = 0
	taskData, _ := json.Marshal(task)
	quotaData, _ := json.Marshal(state)
	quotaKey := s.key("configuration", "quota")
	resp, err := s.client.Txn(ctx).If(cmp, clientv3.Compare(clientv3.ModRevision(quotaKey), "=", quotaKV.ModRevision)).Then(clientv3.OpPut(key, string(taskData)), clientv3.OpPut(quotaKey, string(quotaData))).Commit()
	if err != nil {
		return task, true, err
	}
	if !resp.Succeeded {
		return task, true, storeapi.ErrConflict
	}
	setRevision(&task, resp.Header.Revision)
	return task, true, nil
}

func (s *Store) putCountedWithQuota(ctx context.Context, kind, id string, expected int64, value any) (int64, bool, error) {
	state, quotaKV, err := s.quota(ctx)
	if errors.Is(err, storeapi.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, err
	}
	key := s.key(kind, id)
	existingKV, existingErr := getOne(ctx, s.client, key)
	cmp := clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
	if existingErr == nil {
		if expected <= 0 || existingKV.ModRevision != expected {
			return 0, true, storeapi.ErrConflict
		}
		cmp = clientv3.Compare(clientv3.ModRevision(key), "=", expected)
		data, _ := json.Marshal(value)
		resp, err := s.client.Txn(ctx).If(cmp).Then(clientv3.OpPut(key, string(data))).Commit()
		if err != nil {
			return 0, true, err
		}
		if !resp.Succeeded {
			return 0, true, storeapi.ErrConflict
		}
		return resp.Header.Revision, true, nil
	} else if !errors.Is(existingErr, storeapi.ErrNotFound) {
		return 0, true, existingErr
	} else {
		if expected > 0 {
			return 0, true, storeapi.ErrConflict
		}
		switch kind {
		case "workloads":
			state.Usage.Workloads++
		case "services":
			state.Usage.Services++
		default:
			return 0, true, errors.New("unsupported quota resource kind")
		}
	}
	if err := state.withinLimits(); err != nil {
		return 0, true, err
	}
	data, _ := json.Marshal(value)
	quotaData, _ := json.Marshal(state)
	quotaKey := s.key("configuration", "quota")
	resp, err := s.client.Txn(ctx).If(cmp, clientv3.Compare(clientv3.ModRevision(quotaKey), "=", quotaKV.ModRevision)).Then(clientv3.OpPut(key, string(data)), clientv3.OpPut(quotaKey, string(quotaData))).Commit()
	if err != nil {
		return 0, true, err
	}
	if !resp.Succeeded {
		return 0, true, storeapi.ErrConflict
	}
	return resp.Header.Revision, true, nil
}

func (s *Store) deleteCountedWithQuota(ctx context.Context, kind, id string, expected int64) (bool, error) {
	state, quotaKV, err := s.quota(ctx)
	if errors.Is(err, storeapi.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	key := s.key(kind, id)
	resourceKV, err := getOne(ctx, s.client, key)
	if err != nil {
		return true, err
	}
	if resourceKV.ModRevision != expected {
		return true, storeapi.ErrConflict
	}
	switch kind {
	case "workloads":
		state.Usage.Workloads--
	case "services":
		state.Usage.Services--
	default:
		return true, errors.New("unsupported quota resource kind")
	}
	if err := state.withinLimits(); err != nil {
		return true, err
	}
	quotaData, _ := json.Marshal(state)
	quotaKey := s.key("configuration", "quota")
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", expected), clientv3.Compare(clientv3.ModRevision(quotaKey), "=", quotaKV.ModRevision)).Then(clientv3.OpDelete(key), clientv3.OpPut(quotaKey, string(quotaData))).Commit()
	if err != nil {
		return true, err
	}
	if !resp.Succeeded {
		return true, storeapi.ErrConflict
	}
	return true, nil
}
