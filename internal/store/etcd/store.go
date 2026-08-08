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
	}
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
	assignment := api.Assignment{APIVersion: api.Version, Metadata: api.Metadata{ID: task.Metadata.ID + "/" + fmt.Sprint(gen), Generation: gen}, TaskID: task.Metadata.ID, WorkloadID: task.Spec.WorkloadID, NodeID: node.Metadata.ID, Generation: gen, Resources: task.Spec.Resources, HostPorts: append([]uint16(nil), task.Spec.HostPorts...), CreatedAt: time.Now().UTC()}
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
