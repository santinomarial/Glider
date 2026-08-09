// Package etcd implements Glider's Phase 11 control-plane keyspace and the
// Phase 12 transactional assignment boundary using etcd v3.
package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/santinomarial/glider/internal/api"
	secretapi "github.com/santinomarial/glider/internal/secret"
	storeapi "github.com/santinomarial/glider/internal/store"
)

type Store struct {
	client *clientv3.Client
	prefix string
}

func New(client *clientv3.Client, clusterID string) (*Store, error) {
	if client == nil {
		return nil, errors.New("etcd client is required")
	}
	if !validID(clusterID) {
		return nil, fmt.Errorf("invalid cluster ID %q", clusterID)
	}
	return &Store{client: client, prefix: "/glider/v1/clusters/" + clusterID}, nil
}
func (s *Store) key(kind, id string) string    { return path.Join(s.prefix, kind, id) }
func (s *Store) kindPrefix(kind string) string { return path.Join(s.prefix, kind) + "/" }

func (s *Store) PutTask(ctx context.Context, t api.Task, expectedRevision int64) (api.Task, error) {
	if !validID(t.Metadata.ID) {
		return t, errors.New("invalid task ID")
	}
	if saved, configured, err := s.putTaskWithQuota(ctx, t, expectedRevision); configured {
		return saved, err
	}
	t.APIVersion = api.Version
	t.Metadata.Revision = 0
	return putResource(ctx, s.client, s.key("tasks", t.Metadata.ID), t, expectedRevision)
}
func (s *Store) PutNode(ctx context.Context, n api.Node, expectedRevision int64) (api.Node, error) {
	if !validID(n.Metadata.ID) {
		return n, errors.New("invalid node ID")
	}
	n.APIVersion = api.Version
	n.Metadata.Revision = 0
	return putResource(ctx, s.client, s.key("nodes", n.Metadata.ID), n, expectedRevision)
}
func (s *Store) PutWorkload(ctx context.Context, w api.Workload, expectedRevision int64) (api.Workload, error) {
	if !validID(w.Metadata.ID) {
		return w, errors.New("invalid workload ID")
	}
	w.APIVersion = api.Version
	w.Metadata.Revision = 0
	if revision, configured, err := s.putCountedWithQuota(ctx, "workloads", w.Metadata.ID, expectedRevision, w); configured {
		if err == nil {
			w.Metadata.Revision = revision
		}
		return w, err
	}
	return putResource(ctx, s.client, s.key("workloads", w.Metadata.ID), w, expectedRevision)
}
func (s *Store) PutService(ctx context.Context, service api.Service, expectedRevision int64) (api.Service, error) {
	if !validID(service.Metadata.ID) {
		return service, errors.New("invalid service ID")
	}
	service.APIVersion = api.Version
	service.Metadata.Revision = 0
	if revision, configured, err := s.putCountedWithQuota(ctx, "services", service.Metadata.ID, expectedRevision, service); configured {
		if err == nil {
			service.Metadata.Revision = revision
		}
		return service, err
	}
	return putResource(ctx, s.client, s.key("services", service.Metadata.ID), service, expectedRevision)
}

func putResource[T any](ctx context.Context, c *clientv3.Client, key string, value T, expected int64) (T, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return value, err
	}
	cmp := clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
	if expected > 0 {
		cmp = clientv3.Compare(clientv3.ModRevision(key), "=", expected)
	}
	resp, err := c.Txn(ctx).If(cmp).Then(clientv3.OpPut(key, string(data))).Commit()
	if err != nil {
		return value, err
	}
	if !resp.Succeeded {
		return value, storeapi.ErrConflict
	}
	setRevision(&value, resp.Header.Revision)
	return value, nil
}
func setRevision[T any](v *T, rev int64) {
	switch x := any(v).(type) {
	case *api.Task:
		x.Metadata.Revision = rev
	case *api.Node:
		x.Metadata.Revision = rev
	case *api.Assignment:
		x.Metadata.Revision = rev
	case *api.Workload:
		x.Metadata.Revision = rev
	case *api.Service:
		x.Metadata.Revision = rev
	case *api.Event:
		x.Metadata.Revision = rev
	case *secretapi.Envelope:
		x.Metadata.Revision = rev
	}
}

func (s *Store) PutSecret(ctx context.Context, value secretapi.Envelope, expected int64) (secretapi.Envelope, error) {
	if !validID(value.Metadata.ID) {
		return value, errors.New("invalid secret ID")
	}
	value.Metadata.Revision = 0
	return putResource(ctx, s.client, s.key("secrets", value.Metadata.ID), value, expected)
}

func (s *Store) GetSecret(ctx context.Context, id string) (secretapi.Envelope, error) {
	var value secretapi.Envelope
	if !validID(id) {
		return value, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("secrets", id))
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(kv.Value, &value); err != nil {
		return value, fmt.Errorf("decode secret %s: %w", id, err)
	}
	value.Metadata.Revision = kv.ModRevision
	return value, nil
}

func (s *Store) ListSecrets(ctx context.Context) ([]secretapi.Envelope, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("secrets"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	values := make([]secretapi.Envelope, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var value secretapi.Envelope
		if err := json.Unmarshal(kv.Value, &value); err != nil {
			return nil, err
		}
		value.Metadata.Revision = kv.ModRevision
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Metadata.ID < values[j].Metadata.ID })
	return values, nil
}

func (s *Store) DeleteSecret(ctx context.Context, id string, expected int64) error {
	if !validID(id) {
		return storeapi.ErrNotFound
	}
	key := s.key("secrets", id)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", expected)).Then(clientv3.OpDelete(key)).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		if _, err := getOne(ctx, s.client, key); errors.Is(err, storeapi.ErrNotFound) {
			return storeapi.ErrNotFound
		}
		return storeapi.ErrConflict
	}
	return nil
}
func (s *Store) PutEvent(ctx context.Context, event api.Event) (api.Event, error) {
	if !validID(event.Metadata.ID) {
		return event, errors.New("invalid event ID")
	}
	event.APIVersion = api.Version
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Metadata.Revision = 0
	return putResource(ctx, s.client, s.key("events", event.Metadata.ID), event, 0)
}
func (s *Store) ListEvents(ctx context.Context) ([]api.Event, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("events"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Event, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var event api.Event
		if err := json.Unmarshal(kv.Value, &event); err != nil {
			return nil, err
		}
		event.Metadata.Revision = kv.ModRevision
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].Metadata.ID < out[j].Metadata.ID
		}
		return out[i].Time.Before(out[j].Time)
	})
	return out, nil
}

// PruneEvents enforces both age and count bounds using revision-guarded
// batches. Events are immutable, so a conflict means the next periodic pass
// can safely recompute retention from fresh state.
func (s *Store) PruneEvents(ctx context.Context, before time.Time, max int) (int, error) {
	if max <= 0 {
		return 0, errors.New("event retention maximum must be positive")
	}
	events, err := s.ListEvents(ctx)
	if err != nil {
		return 0, err
	}
	excess := len(events) - max
	if excess < 0 {
		excess = 0
	}
	var doomed []api.Event
	for index, event := range events {
		if index < excess || event.Time.Before(before) {
			doomed = append(doomed, event)
		}
	}
	removed := 0
	for len(doomed) > 0 {
		batch := doomed
		if len(batch) > 64 {
			batch = batch[:64]
		}
		compares := make([]clientv3.Cmp, 0, len(batch))
		operations := make([]clientv3.Op, 0, len(batch))
		for _, event := range batch {
			key := s.key("events", event.Metadata.ID)
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", event.Metadata.Revision))
			operations = append(operations, clientv3.OpDelete(key))
		}
		resp, err := s.client.Txn(ctx).If(compares...).Then(operations...).Commit()
		if err != nil {
			return removed, err
		}
		if !resp.Succeeded {
			return removed, storeapi.ErrConflict
		}
		removed += len(batch)
		doomed = doomed[len(batch):]
	}
	return removed, nil
}
func (s *Store) ListServices(ctx context.Context) ([]api.Service, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("services"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Service, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var service api.Service
		if err := json.Unmarshal(kv.Value, &service); err != nil {
			return nil, err
		}
		service.Metadata.Revision = kv.ModRevision
		out = append(out, service)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (api.Task, error) {
	var t api.Task
	if !validID(id) {
		return t, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("tasks", id))
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(kv.Value, &t); err != nil {
		return t, fmt.Errorf("decode task %s: %w", id, err)
	}
	t.Metadata.Revision = kv.ModRevision
	return t, nil
}
func (s *Store) GetAssignment(ctx context.Context, taskID string) (api.Assignment, error) {
	var assignment api.Assignment
	if !validID(taskID) {
		return assignment, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("assignments", taskID))
	if err != nil {
		return assignment, err
	}
	if err := json.Unmarshal(kv.Value, &assignment); err != nil {
		return assignment, fmt.Errorf("decode assignment %s: %w", taskID, err)
	}
	assignment.Metadata.Revision = kv.ModRevision
	return assignment, nil
}
func (s *Store) GetNode(ctx context.Context, id string) (api.Node, error) {
	var node api.Node
	if !validID(id) {
		return node, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("nodes", id))
	if err != nil {
		return node, err
	}
	if err := json.Unmarshal(kv.Value, &node); err != nil {
		return node, fmt.Errorf("decode node %s: %w", id, err)
	}
	node.Metadata.Revision = kv.ModRevision
	return node, nil
}
func (s *Store) GetWorkload(ctx context.Context, id string) (api.Workload, error) {
	var workload api.Workload
	if !validID(id) {
		return workload, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("workloads", id))
	if err != nil {
		return workload, err
	}
	if err := json.Unmarshal(kv.Value, &workload); err != nil {
		return workload, fmt.Errorf("decode workload %s: %w", id, err)
	}
	workload.Metadata.Revision = kv.ModRevision
	return workload, nil
}
func (s *Store) GetService(ctx context.Context, id string) (api.Service, error) {
	var service api.Service
	if !validID(id) {
		return service, storeapi.ErrNotFound
	}
	kv, err := getOne(ctx, s.client, s.key("services", id))
	if err != nil {
		return service, err
	}
	if err := json.Unmarshal(kv.Value, &service); err != nil {
		return service, fmt.Errorf("decode service %s: %w", id, err)
	}
	service.Metadata.Revision = kv.ModRevision
	return service, nil
}
func (s *Store) ListTasks(ctx context.Context) ([]api.Task, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("tasks"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Task, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var task api.Task
		if err := json.Unmarshal(kv.Value, &task); err != nil {
			return nil, err
		}
		task.Metadata.Revision = kv.ModRevision
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}
func (s *Store) ListWorkloads(ctx context.Context) ([]api.Workload, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("workloads"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Workload, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var w api.Workload
		if err := json.Unmarshal(kv.Value, &w); err != nil {
			return nil, err
		}
		w.Metadata.Revision = kv.ModRevision
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}

func (s *Store) DeleteWorkload(ctx context.Context, id string, expected int64) error {
	if !validID(id) {
		return storeapi.ErrNotFound
	}
	if configured, err := s.deleteCountedWithQuota(ctx, "workloads", id, expected); configured {
		return err
	}
	key := s.key("workloads", id)
	cmp := clientv3.Compare(clientv3.ModRevision(key), "=", expected)
	resp, err := s.client.Txn(ctx).If(cmp).Then(clientv3.OpDelete(key)).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		if _, err := getOne(ctx, s.client, key); errors.Is(err, storeapi.ErrNotFound) {
			return storeapi.ErrNotFound
		}
		return storeapi.ErrConflict
	}
	return nil
}

func (s *Store) DeleteService(ctx context.Context, id string, expected int64) error {
	if !validID(id) {
		return storeapi.ErrNotFound
	}
	if configured, err := s.deleteCountedWithQuota(ctx, "services", id, expected); configured {
		return err
	}
	key := s.key("services", id)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(key), "=", expected)).Then(clientv3.OpDelete(key)).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		if _, err := getOne(ctx, s.client, key); errors.Is(err, storeapi.ErrNotFound) {
			return storeapi.ErrNotFound
		}
		return storeapi.ErrConflict
	}
	return nil
}

// DeleteTask removes a task and assignment and releases its reservation in one
// transaction, so replica scale-down cannot leak capacity or race a bind.
func (s *Store) DeleteTask(ctx context.Context, id string, expected int64) error {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if expected > 0 && task.Metadata.Revision != expected {
		return storeapi.ErrConflict
	}
	quotaState, quotaKV, quotaErr := s.quota(ctx)
	quotaConfigured := quotaErr == nil
	if quotaErr != nil && !errors.Is(quotaErr, storeapi.ErrNotFound) {
		return quotaErr
	}
	var quotaData []byte
	quotaKey := s.key("configuration", "quota")
	if quotaConfigured {
		quotaState.Usage.Tasks--
		quotaState.Usage.Resources = quotaState.Usage.Resources.Sub(task.Spec.Resources)
		if err := quotaState.withinLimits(); err != nil {
			return err
		}
		quotaData, _ = json.Marshal(quotaState)
	}
	taskKey, assignmentKey := s.key("tasks", id), s.key("assignments", id)
	assignmentKV, getErr := getOne(ctx, s.client, assignmentKey)
	if errors.Is(getErr, storeapi.ErrNotFound) {
		compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(taskKey), "=", task.Metadata.Revision)}
		operations := []clientv3.Op{clientv3.OpDelete(taskKey)}
		if quotaConfigured {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(quotaKey), "=", quotaKV.ModRevision))
			operations = append(operations, clientv3.OpPut(quotaKey, string(quotaData)))
		}
		resp, err := s.client.Txn(ctx).If(compares...).Then(operations...).Commit()
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			return storeapi.ErrConflict
		}
		return nil
	}
	if getErr != nil {
		return getErr
	}
	var assignment api.Assignment
	if err := json.Unmarshal(assignmentKV.Value, &assignment); err != nil {
		return err
	}
	nodeKey := s.key("nodes", assignment.NodeID)
	nodeKV, err := getOne(ctx, s.client, nodeKey)
	if err != nil {
		return err
	}
	var node api.Node
	if err := json.Unmarshal(nodeKV.Value, &node); err != nil {
		return err
	}
	node.Status.Reserved = node.Status.Reserved.Sub(assignment.Resources)
	if node.Status.Reserved.CPUMilli < 0 || node.Status.Reserved.MemoryBytes < 0 {
		return errors.New("corrupt negative node reservation")
	}
	node.Metadata.Revision = 0
	nodeData, _ := json.Marshal(node)
	compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(taskKey), "=", task.Metadata.Revision), clientv3.Compare(clientv3.ModRevision(assignmentKey), "=", assignmentKV.ModRevision), clientv3.Compare(clientv3.ModRevision(nodeKey), "=", nodeKV.ModRevision)}
	operations := []clientv3.Op{clientv3.OpDelete(taskKey), clientv3.OpDelete(assignmentKey), clientv3.OpPut(nodeKey, string(nodeData))}
	if quotaConfigured {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(quotaKey), "=", quotaKV.ModRevision))
		operations = append(operations, clientv3.OpPut(quotaKey, string(quotaData)))
	}
	resp, err := s.client.Txn(ctx).If(compares...).Then(operations...).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return storeapi.ErrConflict
	}
	return nil
}

// EvictNodeAssignments generation-safely returns tasks to PENDING while
// releasing reservations. Each assignment is an independent CAS transaction;
// conflicts are left for the next level-triggered monitor pass.
func (s *Store) EvictNodeAssignments(ctx context.Context, nodeID string) error {
	assignments, err := s.ListAssignments(ctx)
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.NodeID != nodeID {
			continue
		}
		task, err := s.GetTask(ctx, assignment.TaskID)
		if err != nil {
			continue
		}
		taskRevision := task.Metadata.Revision
		nodeKV, err := getOne(ctx, s.client, s.key("nodes", nodeID))
		if err != nil {
			return err
		}
		var node api.Node
		if err := json.Unmarshal(nodeKV.Value, &node); err != nil {
			return err
		}
		assignmentKV, err := getOne(ctx, s.client, s.key("assignments", assignment.TaskID))
		if err != nil {
			continue
		}
		var current api.Assignment
		if err := json.Unmarshal(assignmentKV.Value, &current); err != nil {
			return err
		}
		if current.Generation != assignment.Generation || task.Status.AssignmentGeneration != assignment.Generation {
			continue
		}
		node.Status.Reserved = node.Status.Reserved.Sub(assignment.Resources)
		if node.Status.Reserved.CPUMilli < 0 || node.Status.Reserved.MemoryBytes < 0 {
			return errors.New("corrupt negative node reservation")
		}
		node.Metadata.Revision = 0
		task.Status.Phase = api.TaskPending
		task.Status.NodeID = ""
		task.Status.Address = ""
		task.Status.Ready = false
		task.Metadata.Revision = 0
		nodeData, _ := json.Marshal(node)
		taskData, _ := json.Marshal(task)
		resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.key("tasks", task.Metadata.ID)), "=", taskRevision), clientv3.Compare(clientv3.ModRevision(s.key("nodes", nodeID)), "=", nodeKV.ModRevision), clientv3.Compare(clientv3.ModRevision(s.key("assignments", assignment.TaskID)), "=", assignmentKV.ModRevision)).Then(clientv3.OpPut(s.key("tasks", task.Metadata.ID), string(taskData)), clientv3.OpPut(s.key("nodes", nodeID), string(nodeData)), clientv3.OpDelete(s.key("assignments", assignment.TaskID))).Commit()
		if err != nil {
			return err
		}
		if !resp.Succeeded {
			continue
		}
	}
	return nil
}
func (s *Store) ReportTaskHealth(ctx context.Context, taskID string, generation int64, ready bool) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status.AssignmentGeneration != generation {
		return storeapi.ErrConflict
	}
	if task.Status.Ready == ready {
		return nil
	}
	revision := task.Metadata.Revision
	task.Metadata.Revision = 0
	task.Status.Ready = ready
	task.Status.LastHealthTransition = time.Now().UTC()
	data, _ := json.Marshal(task)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.key("tasks", taskID)), "=", revision)).Then(clientv3.OpPut(s.key("tasks", taskID), string(data))).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return storeapi.ErrConflict
	}
	return nil
}
func (s *Store) ReportTaskEndpoint(ctx context.Context, taskID string, generation int64, address string) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status.AssignmentGeneration != generation {
		return storeapi.ErrConflict
	}
	if task.Status.Address == address {
		return nil
	}
	revision := task.Metadata.Revision
	task.Metadata.Revision = 0
	task.Status.Address = address
	data, _ := json.Marshal(task)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.key("tasks", taskID)), "=", revision)).Then(clientv3.OpPut(s.key("tasks", taskID), string(data))).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return storeapi.ErrConflict
	}
	return nil
}

// RestartTask revokes exactly the observed generation and returns the task to
// PENDING. A stale health result can never revoke a newer assignment.
func (s *Store) RestartTask(ctx context.Context, taskID string, generation int64) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status.AssignmentGeneration != generation {
		return storeapi.ErrConflict
	}
	assignmentKV, err := getOne(ctx, s.client, s.key("assignments", taskID))
	if err != nil {
		return err
	}
	var assignment api.Assignment
	if err := json.Unmarshal(assignmentKV.Value, &assignment); err != nil {
		return err
	}
	if assignment.Generation != generation {
		return storeapi.ErrConflict
	}
	nodeKV, err := getOne(ctx, s.client, s.key("nodes", assignment.NodeID))
	if err != nil {
		return err
	}
	var node api.Node
	if err := json.Unmarshal(nodeKV.Value, &node); err != nil {
		return err
	}
	node.Status.Reserved = node.Status.Reserved.Sub(assignment.Resources)
	if node.Status.Reserved.CPUMilli < 0 || node.Status.Reserved.MemoryBytes < 0 {
		return errors.New("corrupt negative node reservation")
	}
	taskRevision := task.Metadata.Revision
	task.Metadata.Revision = 0
	task.Status.Phase = api.TaskPending
	task.Status.NodeID = ""
	task.Status.Address = ""
	task.Status.Ready = false
	task.Status.RestartCount++
	node.Metadata.Revision = 0
	taskData, _ := json.Marshal(task)
	nodeData, _ := json.Marshal(node)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.key("tasks", taskID)), "=", taskRevision), clientv3.Compare(clientv3.ModRevision(s.key("assignments", taskID)), "=", assignmentKV.ModRevision), clientv3.Compare(clientv3.ModRevision(s.key("nodes", assignment.NodeID)), "=", nodeKV.ModRevision)).Then(clientv3.OpPut(s.key("tasks", taskID), string(taskData)), clientv3.OpPut(s.key("nodes", assignment.NodeID), string(nodeData)), clientv3.OpDelete(s.key("assignments", taskID))).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return storeapi.ErrConflict
	}
	return nil
}
func (s *Store) ListNodes(ctx context.Context) ([]api.Node, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("nodes"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Node, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var n api.Node
		if err := json.Unmarshal(kv.Value, &n); err != nil {
			return nil, fmt.Errorf("decode node: %w", err)
		}
		n.Metadata.Revision = kv.ModRevision
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}
func (s *Store) ListAssignments(ctx context.Context) ([]api.Assignment, error) {
	resp, err := s.client.Get(ctx, s.kindPrefix("assignments"), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]api.Assignment, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var a api.Assignment
		if err := json.Unmarshal(kv.Value, &a); err != nil {
			return nil, fmt.Errorf("decode assignment: %w", err)
		}
		a.Metadata.Revision = kv.ModRevision
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out, nil
}

func (s *Store) Snapshot(ctx context.Context, nodeID string) ([]api.Assignment, error) {
	all, err := s.ListAssignments(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, a := range all {
		if a.NodeID == nodeID {
			out = append(out, a)
		}
	}
	return out, nil
}
func (s *Store) Watch(ctx context.Context, nodeID string) (<-chan struct{}, error) {
	if !validID(nodeID) {
		return nil, errors.New("invalid node ID")
	}
	out := make(chan struct{}, 1)
	watch := s.client.Watch(ctx, s.kindPrefix("assignments"), clientv3.WithPrefix())
	go func() {
		defer close(out)
		for response := range watch {
			if response.Err() != nil {
				return
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, nil
}

func (s *Store) Bind(ctx context.Context, r storeapi.BindRequest) (api.Assignment, error) {
	task, err := s.GetTask(ctx, r.TaskID)
	if err != nil {
		return api.Assignment{}, err
	}
	nodeKV, err := getOne(ctx, s.client, s.key("nodes", r.NodeID))
	if err != nil {
		return api.Assignment{}, err
	}
	var node api.Node
	if err := json.Unmarshal(nodeKV.Value, &node); err != nil {
		return api.Assignment{}, err
	}
	node.Metadata.Revision = nodeKV.ModRevision
	if task.Metadata.Revision != r.TaskRevision || node.Metadata.Revision != r.NodeRevision {
		return api.Assignment{}, storeapi.ErrConflict
	}
	if task.Status.Phase != api.TaskPending {
		return api.Assignment{}, storeapi.ErrAlreadyAssigned
	}
	if node.Status.Phase != api.NodeReady || node.Spec.Unschedulable || !node.Available().Fits(task.Spec.Resources) {
		return api.Assignment{}, storeapi.ErrInsufficientCapacity
	}
	gen := task.Metadata.Generation + 1
	assignment := api.Assignment{APIVersion: api.Version, Metadata: api.Metadata{ID: task.Metadata.ID + "/" + fmt.Sprint(gen), Generation: gen}, TaskID: task.Metadata.ID, WorkloadID: task.Spec.WorkloadID, NodeID: node.Metadata.ID, Generation: gen, Resources: task.Spec.Resources, Image: task.Spec.Image, Command: append([]string(nil), task.Spec.Command...), RestartPolicy: task.Spec.RestartPolicy, Health: task.Spec.Health, HostPorts: append([]uint16(nil), task.Spec.HostPorts...), Secrets: append([]api.SecretEnvRef(nil), task.Spec.Secrets...), CreatedAt: time.Now().UTC()}
	task.Status.Phase = api.TaskScheduled
	task.Status.NodeID = node.Metadata.ID
	task.Status.AssignmentGeneration = gen
	task.Metadata.Generation = gen
	task.Metadata.Revision = 0
	node.Status.Reserved = node.Status.Reserved.Add(task.Spec.Resources)
	node.Metadata.Revision = 0
	taskData, _ := json.Marshal(task)
	nodeData, _ := json.Marshal(node)
	assignmentData, _ := json.Marshal(assignment)
	assignmentKey := s.key("assignments", task.Metadata.ID)
	resp, err := s.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.key("tasks", task.Metadata.ID)), "=", r.TaskRevision), clientv3.Compare(clientv3.ModRevision(s.key("nodes", node.Metadata.ID)), "=", r.NodeRevision), clientv3.Compare(clientv3.CreateRevision(assignmentKey), "=", 0)).Then(clientv3.OpPut(s.key("tasks", task.Metadata.ID), string(taskData)), clientv3.OpPut(s.key("nodes", node.Metadata.ID), string(nodeData)), clientv3.OpPut(assignmentKey, string(assignmentData))).Commit()
	if err != nil {
		return api.Assignment{}, err
	}
	if !resp.Succeeded {
		if existing, err := s.client.Get(ctx, assignmentKey); err == nil && len(existing.Kvs) > 0 {
			return api.Assignment{}, storeapi.ErrAlreadyAssigned
		}
		return api.Assignment{}, storeapi.ErrConflict
	}
	assignment.Metadata.Revision = resp.Header.Revision
	return assignment, nil
}

func getOne(ctx context.Context, c *clientv3.Client, key string) (*mvccpb.KeyValue, error) {
	resp, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, storeapi.ErrNotFound
	}
	return resp.Kvs[0], nil
}
func validID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\\\x00")
}
