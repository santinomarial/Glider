// Package nodeops exposes generation-fenced node-local operations.
package nodeops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const ServiceName = "glider.v1.NodeOperations"

type AssignmentLister interface {
	ListAssignments(context.Context) ([]api.Assignment, error)
}
type Runtime interface {
	Logs(api.Assignment, int64) ([]byte, error)
	Stats(api.Assignment) (cgroup.Stats, error)
}
type Service struct {
	nodeID  string
	store   AssignmentLister
	runtime Runtime
}

func New(nodeID string, store AssignmentLister, runtime Runtime) (*Service, error) {
	if nodeID == "" || store == nil || runtime == nil {
		return nil, errors.New("node ID, store, and runtime are required")
	}
	return &Service{nodeID: nodeID, store: store, runtime: runtime}, nil
}
func (s *Service) assignment(ctx context.Context, in *structpb.Struct) (api.Assignment, error) {
	taskID, ok := in.AsMap()["task_id"].(string)
	if !ok || taskID == "" {
		return api.Assignment{}, status.Error(codes.InvalidArgument, "task_id is required")
	}
	generation, ok := in.AsMap()["generation"].(float64)
	if !ok || generation <= 0 {
		return api.Assignment{}, status.Error(codes.InvalidArgument, "positive generation is required")
	}
	values, err := s.store.ListAssignments(ctx)
	if err != nil {
		return api.Assignment{}, status.Error(codes.Unavailable, err.Error())
	}
	for _, a := range values {
		if a.TaskID == taskID && a.Generation == int64(generation) && a.NodeID == s.nodeID {
			return a, nil
		}
	}
	return api.Assignment{}, status.Error(codes.FailedPrecondition, "assignment is absent, stale, or owned by another node")
}
func (s *Service) GetLogs(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	a, err := s.assignment(ctx, in)
	if err != nil {
		return nil, err
	}
	tail := int64(64 << 10)
	if value, ok := in.AsMap()["tail_bytes"].(float64); ok {
		tail = int64(value)
	}
	data, err := s.runtime.Logs(a, tail)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return structpb.NewStruct(map[string]any{"task_id": a.TaskID, "generation": a.Generation, "data_base64": base64.StdEncoding.EncodeToString(data)})
}
func (s *Service) GetStats(ctx context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	a, err := s.assignment(ctx, in)
	if err != nil {
		return nil, err
	}
	value, err := s.runtime.Stats(a)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	data, _ := json.Marshal(value)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	object["task_id"] = a.TaskID
	object["generation"] = a.Generation
	return structpb.NewStruct(object)
}

type server interface {
	GetLogs(context.Context, *structpb.Struct) (*structpb.Struct, error)
	GetStats(context.Context, *structpb.Struct) (*structpb.Struct, error)
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

var description = grpc.ServiceDesc{ServiceName: ServiceName, HandlerType: (*server)(nil), Methods: []grpc.MethodDesc{unary("GetLogs", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
	return s.GetLogs(c, r)
}), unary("GetStats", func(s server, c context.Context, r *structpb.Struct) (*structpb.Struct, error) {
	return s.GetStats(c, r)
})}}

func Register(registrar grpc.ServiceRegistrar, implementation server) {
	registrar.RegisterService(&description, implementation)
}

var _ = fmt.Sprintf
