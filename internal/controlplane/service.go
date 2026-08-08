// Package controlplane exposes Glider's versioned gRPC API over authoritative
// etcd state. All mutation conflicts map to gRPC Aborted for safe client retry.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/scheduler"
	storeapi "github.com/santinomarial/glider/internal/store"
	etcdstore "github.com/santinomarial/glider/internal/store/etcd"
)

const ServiceName = "glider.v1.ControlPlane"

type Service struct {
	store     *etcdstore.Store
	scheduler *scheduler.Controller
}

func New(store *etcdstore.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	s, err := scheduler.NewController(store)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, scheduler: s}, nil
}

func (s *Service) PutTask(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	var task api.Task
	if err := decode(in, &task); err != nil {
		return nil, invalid(err)
	}
	saved, err := s.store.PutTask(ctx, task, task.Metadata.Revision)
	return encode(saved, mapError(err))
}
func (s *Service) PutNode(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	var node api.Node
	if err := decode(in, &node); err != nil {
		return nil, invalid(err)
	}
	saved, err := s.store.PutNode(ctx, node, node.Metadata.Revision)
	return encode(saved, mapError(err))
}
func (s *Service) GetTask(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	id, err := requiredString(in, "id")
	if err != nil {
		return nil, invalid(err)
	}
	value, err := s.store.GetTask(ctx, id)
	return encode(value, mapError(err))
}
func (s *Service) ListNodes(ctx context.Context, _ *structpb.Struct) (*structpb.Struct, error) {
	values, err := s.store.ListNodes(ctx)
	return encode(map[string]any{"items": values}, mapError(err))
}
func (s *Service) ListAssignments(ctx context.Context, _ *structpb.Struct) (*structpb.Struct, error) {
	values, err := s.store.ListAssignments(ctx)
	return encode(map[string]any{"items": values}, mapError(err))
}
func (s *Service) PutWorkload(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	var workload api.Workload
	if err := decode(in, &workload); err != nil {
		return nil, invalid(err)
	}
	saved, err := s.store.PutWorkload(ctx, workload, workload.Metadata.Revision)
	return encode(saved, mapError(err))
}
func (s *Service) ListWorkloads(ctx context.Context, _ *structpb.Struct) (*structpb.Struct, error) {
	values, err := s.store.ListWorkloads(ctx)
	return encode(map[string]any{"items": values}, mapError(err))
}
func (s *Service) Schedule(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	id, err := requiredString(in, "task_id")
	if err != nil {
		return nil, invalid(err)
	}
	value, err := s.scheduler.ScheduleOne(ctx, id)
	return encode(value, mapError(err))
}

func decode(in *structpb.Struct, out any) error {
	if in == nil {
		return errors.New("request is required")
	}
	data, err := json.Marshal(in.AsMap())
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func encode(value any, err error) (*structpb.Struct, error) {
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	var object map[string]any
	if err = json.Unmarshal(data, &object); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	result, err := structpb.NewStruct(object)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return result, nil
}
func requiredString(in *structpb.Struct, key string) (string, error) {
	if in == nil {
		return "", errors.New("request is required")
	}
	value, ok := in.AsMap()[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
func invalid(err error) error { return status.Error(codes.InvalidArgument, err.Error()) }
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storeapi.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, storeapi.ErrConflict), errors.Is(err, storeapi.ErrAlreadyAssigned):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, storeapi.ErrInsufficientCapacity), errors.Is(err, scheduler.ErrUnschedulable):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

type server interface {
	PutTask(context.Context, *structpb.Struct) (*structpb.Struct, error)
	PutNode(context.Context, *structpb.Struct) (*structpb.Struct, error)
	GetTask(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ListNodes(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ListAssignments(context.Context, *structpb.Struct) (*structpb.Struct, error)
	PutWorkload(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ListWorkloads(context.Context, *structpb.Struct) (*structpb.Struct, error)
	Schedule(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func Register(registrar grpc.ServiceRegistrar, implementation server) {
	registrar.RegisterService(&description, implementation)
}
func unary(method string, call func(server, context.Context, *structpb.Struct) (*structpb.Struct, error)) grpc.MethodDesc {
	return grpc.MethodDesc{MethodName: method, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(structpb.Struct)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return call(srv.(server), ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + ServiceName + "/" + method}
		handler := func(ctx context.Context, req any) (any, error) {
			return call(srv.(server), ctx, req.(*structpb.Struct))
		}
		return interceptor(ctx, in, info, handler)
	}}
}

var description = grpc.ServiceDesc{ServiceName: ServiceName, HandlerType: (*server)(nil), Methods: []grpc.MethodDesc{
	unary("PutTask", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.PutTask(c, r)
	}),
	unary("PutNode", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.PutNode(c, r)
	}),
	unary("GetTask", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.GetTask(c, r)
	}),
	unary("ListNodes", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.ListNodes(c, r)
	}),
	unary("ListAssignments", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.ListAssignments(c, r)
	}),
	unary("PutWorkload", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.PutWorkload(c, r)
	}),
	unary("ListWorkloads", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.ListWorkloads(c, r)
	}),
	unary("Schedule", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
		return s.Schedule(c, r)
	}),
}}
