package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// V2 adapts the compatibility-enforced typed API to the same authoritative
// implementation used by the legacy v1 Struct service. Keeping one mutation
// path prevents validation, authorization, and transaction semantics from
// drifting between API versions during migration.
type V2 struct {
	gliderv2.UnimplementedControlPlaneServiceServer
	legacy *Service
}

func RegisterV2(registrar grpc.ServiceRegistrar, legacy *Service) {
	gliderv2.RegisterControlPlaneServiceServer(registrar, &V2{legacy: legacy})
}

func (s *V2) PutTask(ctx context.Context, request *gliderv2.PutTaskRequest) (*gliderv2.PutTaskResponse, error) {
	in, err := typedResource(request.GetTask())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutTask(ctx, in)
	response := new(gliderv2.PutTaskResponse)
	return response, typedResponse(out, "task", response, err)
}

func (s *V2) DeleteTask(ctx context.Context, request *gliderv2.DeleteTaskRequest) (*gliderv2.DeleteTaskResponse, error) {
	_, err := s.legacy.DeleteTask(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	if err != nil {
		return nil, err
	}
	return &gliderv2.DeleteTaskResponse{Result: &gliderv2.DeleteResult{Id: request.GetId(), Deleted: true}}, nil
}

func (s *V2) PutNode(ctx context.Context, request *gliderv2.PutNodeRequest) (*gliderv2.PutNodeResponse, error) {
	in, err := typedResource(request.GetNode())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutNode(ctx, in)
	response := new(gliderv2.PutNodeResponse)
	return response, typedResponse(out, "node", response, err)
}

func (s *V2) GetTask(ctx context.Context, request *gliderv2.GetTaskRequest) (*gliderv2.GetTaskResponse, error) {
	out, err := s.legacy.GetTask(ctx, fields("id", request.GetId()))
	response := new(gliderv2.GetTaskResponse)
	return response, typedResponse(out, "task", response, err)
}

func (s *V2) ListTasks(ctx context.Context, request *gliderv2.ListTasksRequest) (*gliderv2.ListTasksResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListTasks(ctx, &structpb.Struct{})
	response := new(gliderv2.ListTasksResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) ListNodes(ctx context.Context, request *gliderv2.ListNodesRequest) (*gliderv2.ListNodesResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListNodes(ctx, &structpb.Struct{})
	response := new(gliderv2.ListNodesResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) DrainNode(ctx context.Context, request *gliderv2.DrainNodeRequest) (*gliderv2.DrainNodeResponse, error) {
	out, err := s.legacy.DrainNode(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	response := new(gliderv2.DrainNodeResponse)
	return response, typedResponse(out, "node", response, err)
}

func (s *V2) RemoveNode(ctx context.Context, request *gliderv2.RemoveNodeRequest) (*gliderv2.RemoveNodeResponse, error) {
	_, err := s.legacy.RemoveNode(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	if err != nil {
		return nil, err
	}
	return &gliderv2.RemoveNodeResponse{Result: &gliderv2.DeleteResult{Id: request.GetId(), Deleted: true}}, nil
}

func (s *V2) ListAssignments(ctx context.Context, request *gliderv2.ListAssignmentsRequest) (*gliderv2.ListAssignmentsResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListAssignments(ctx, &structpb.Struct{})
	response := new(gliderv2.ListAssignmentsResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) PutWorkload(ctx context.Context, request *gliderv2.PutWorkloadRequest) (*gliderv2.PutWorkloadResponse, error) {
	in, err := typedResource(request.GetWorkload())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutWorkload(ctx, in)
	response := new(gliderv2.PutWorkloadResponse)
	return response, typedResponse(out, "workload", response, err)
}

func (s *V2) DeleteWorkload(ctx context.Context, request *gliderv2.DeleteWorkloadRequest) (*gliderv2.DeleteWorkloadResponse, error) {
	out, err := s.legacy.DeleteWorkload(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	response := new(gliderv2.DeleteWorkloadResponse)
	return response, typedResponse(out, "workload", response, err)
}

func (s *V2) ListWorkloads(ctx context.Context, request *gliderv2.ListWorkloadsRequest) (*gliderv2.ListWorkloadsResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListWorkloads(ctx, &structpb.Struct{})
	response := new(gliderv2.ListWorkloadsResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) PutService(ctx context.Context, request *gliderv2.PutServiceRequest) (*gliderv2.PutServiceResponse, error) {
	in, err := typedResource(request.GetService())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutService(ctx, in)
	response := new(gliderv2.PutServiceResponse)
	return response, typedResponse(out, "service", response, err)
}

func (s *V2) DeleteService(ctx context.Context, request *gliderv2.DeleteServiceRequest) (*gliderv2.DeleteServiceResponse, error) {
	_, err := s.legacy.DeleteService(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	if err != nil {
		return nil, err
	}
	return &gliderv2.DeleteServiceResponse{Result: &gliderv2.DeleteResult{Id: request.GetId(), Deleted: true}}, nil
}

func (s *V2) ListServices(ctx context.Context, request *gliderv2.ListServicesRequest) (*gliderv2.ListServicesResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListServices(ctx, &structpb.Struct{})
	response := new(gliderv2.ListServicesResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) PutSecret(ctx context.Context, request *gliderv2.PutSecretRequest) (*gliderv2.PutSecretResponse, error) {
	in, err := typedResource(request.GetSecret())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutSecret(ctx, in)
	response := new(gliderv2.PutSecretResponse)
	if err = typedResponse(out, "secret", response, err); err != nil {
		return nil, err
	}
	for key := range request.GetSecret().GetData() {
		response.Secret.Keys = append(response.Secret.Keys, key)
	}
	sort.Strings(response.Secret.Keys)
	return response, nil
}

func (s *V2) ListSecrets(ctx context.Context, request *gliderv2.ListSecretsRequest) (*gliderv2.ListSecretsResponse, error) {
	if err := validateListOptions(request.GetOptions()); err != nil {
		return nil, err
	}
	out, err := s.legacy.ListSecrets(ctx, &structpb.Struct{})
	response := new(gliderv2.ListSecretsResponse)
	return response, typedResponse(out, "", response, err)
}

func (s *V2) DeleteSecret(ctx context.Context, request *gliderv2.DeleteSecretRequest) (*gliderv2.DeleteSecretResponse, error) {
	_, err := s.legacy.DeleteSecret(ctx, fields("id", request.GetId(), "revision", request.GetRevision()))
	if err != nil {
		return nil, err
	}
	return &gliderv2.DeleteSecretResponse{Result: &gliderv2.DeleteResult{Id: request.GetId(), Deleted: true}}, nil
}

func (s *V2) GetAssignmentSecrets(ctx context.Context, request *gliderv2.GetAssignmentSecretsRequest) (*gliderv2.GetAssignmentSecretsResponse, error) {
	out, err := s.legacy.GetAssignmentSecrets(ctx, fields("task_id", request.GetTaskId(), "generation", request.GetGeneration()))
	if err != nil {
		return nil, err
	}
	response := &gliderv2.GetAssignmentSecretsResponse{TaskId: request.GetTaskId(), Generation: request.GetGeneration(), Environment: map[string][]byte{}}
	values, _ := out.AsMap()["values"].(map[string]any)
	for key, raw := range values {
		encoded, ok := raw.(string)
		if !ok {
			return nil, status.Error(codes.Internal, "legacy secret response is malformed")
		}
		value, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, status.Error(codes.Internal, "legacy secret response encoding is malformed")
		}
		response.Environment[key] = value
	}
	return response, nil
}

func (s *V2) PutEvent(ctx context.Context, request *gliderv2.PutEventRequest) (*gliderv2.PutEventResponse, error) {
	in, err := typedResource(request.GetEvent())
	if err != nil {
		return nil, err
	}
	out, err := s.legacy.PutEvent(ctx, in)
	response := new(gliderv2.PutEventResponse)
	return response, typedResponse(out, "event", response, err)
}

func (s *V2) ListEvents(ctx context.Context, request *gliderv2.ListEventsRequest) (*gliderv2.ListEventsResponse, error) {
	if request.GetPageSize() != 0 || request.GetPageToken() != "" {
		return nil, status.Error(codes.InvalidArgument, "pagination is not enabled; page_size and page_token must be empty")
	}
	out, err := s.legacy.ListEvents(ctx, &structpb.Struct{})
	response := new(gliderv2.ListEventsResponse)
	if err = typedResponse(out, "", response, err); err != nil {
		return nil, err
	}
	filtered := response.Items[:0]
	for _, event := range response.Items {
		if (request.GetObjectKind() != "" && event.GetObjectKind() != request.GetObjectKind()) || (request.GetObjectId() != "" && event.GetObjectId() != request.GetObjectId()) || (request.GetNodeId() != "" && event.GetNodeId() != request.GetNodeId()) {
			continue
		}
		filtered = append(filtered, event)
	}
	response.Items = filtered
	return response, nil
}

func (s *V2) Schedule(ctx context.Context, request *gliderv2.ScheduleRequest) (*gliderv2.ScheduleResponse, error) {
	out, err := s.legacy.Schedule(ctx, fields("task_id", request.GetTaskId()))
	response := new(gliderv2.ScheduleResponse)
	return response, typedResponse(out, "assignment", response, err)
}

func fields(values ...any) *structpb.Struct {
	object := make(map[string]any, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		object[values[index].(string)] = values[index+1]
	}
	result, _ := structpb.NewStruct(object)
	return result
}

func validateListOptions(options *gliderv2.ListOptions) error {
	if options == nil {
		return nil
	}
	if options.GetPageSize() != 0 || options.GetPageToken() != "" || len(options.GetLabelSelector()) != 0 {
		return status.Error(codes.InvalidArgument, "list options are reserved; page_size, page_token, and label_selector must be empty")
	}
	return nil
}

func typedResource(message proto.Message) (*structpb.Struct, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	normalized, err := protoToLegacyMap(object)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	result, err := structpb.NewStruct(normalized)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return result, nil
}

func typedResponse(source *structpb.Struct, wrapper string, destination proto.Message, sourceErr error) error {
	if sourceErr != nil {
		return sourceErr
	}
	if source == nil {
		return status.Error(codes.Internal, "legacy service returned an empty response")
	}
	object := legacyToProtoMap(source.AsMap())
	if wrapper != "" {
		object = map[string]any{wrapper: object}
	}
	data, err := json.Marshal(object)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, destination); err != nil {
		return status.Error(codes.Internal, "convert legacy response to v2: "+err.Error())
	}
	return nil
}

func protoToLegacyMap(input map[string]any) (map[string]any, error) {
	output := make(map[string]any, len(input))
	for key, value := range input {
		legacyKey := protoToLegacyKey(key)
		normalized, err := protoToLegacyValue(key, value)
		if err != nil {
			return nil, err
		}
		output[legacyKey] = normalized
	}
	return output, nil
}

func protoToLegacyValue(key string, value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if key == "attributes" || key == "data" || key == "labels" || key == "node_selector" || key == "selector" || key == "environment" {
			return typed, nil
		}
		return protoToLegacyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := protoToLegacyValue(key, item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case string:
		if isDurationKey(key) {
			duration, err := time.ParseDuration(typed)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", key, err)
			}
			return float64(duration), nil
		}
		if isSigned64Key(key) {
			integer, err := strconv.ParseInt(typed, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", key, err)
			}
			return float64(integer), nil
		}
		if isUnsigned64Key(key) {
			integer, err := strconv.ParseUint(typed, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", key, err)
			}
			return float64(integer), nil
		}
		return protoEnumToLegacy(key, typed), nil
	default:
		return value, nil
	}
}

func legacyToProtoMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		if key == "apiVersion" {
			continue
		}
		protoKey := legacyToProtoKey(key)
		output[protoKey] = legacyToProtoValue(key, value)
	}
	return output
}

func legacyToProtoValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if key == "fields" {
			result := make(map[string]any, len(typed))
			for field, raw := range typed {
				if text, ok := raw.(string); ok {
					result[field] = text
					continue
				}
				encoded, _ := json.Marshal(raw)
				result[field] = string(encoded)
			}
			return result
		}
		if key == "data" || key == "labels" || key == "node_selector" || key == "selector" || key == "env" {
			return typed
		}
		return legacyToProtoMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = legacyToProtoValue(key, item)
		}
		return result
	case string:
		return legacyEnumToProto(key, typed)
	case float64:
		if isDurationKey(key) {
			return time.Duration(int64(typed)).String()
		}
		if isSigned64Key(key) {
			return strconv.FormatInt(int64(typed), 10)
		}
		if isUnsigned64Key(key) {
			return strconv.FormatUint(uint64(typed), 10)
		}
		return value
	default:
		return value
	}
}

func protoToLegacyKey(key string) string {
	return mapLookup(map[string]string{
		"deletion_time":               "deletion_timestamp",
		"environment_variable":        "env",
		"last_health_transition_time": "last_health_transition",
		"start_time":                  "started_at",
		"finish_time":                 "finished_at",
		"restart_not_before_time":     "restart_not_before",
		"update_time":                 "updated_at",
		"rollout_started_time":        "rollout_started_at",
		"last_progress_time":          "last_progress_at",
		"create_time":                 "created_at",
		"attributes":                  "fields",
	}, key)
}

func legacyToProtoKey(key string) string {
	return mapLookup(map[string]string{
		"deletion_timestamp":     "deletion_time",
		"env":                    "environment_variable",
		"last_health_transition": "last_health_transition_time",
		"started_at":             "start_time",
		"finished_at":            "finish_time",
		"restart_not_before":     "restart_not_before_time",
		"updated_at":             "update_time",
		"rollout_started_at":     "rollout_started_time",
		"last_progress_at":       "last_progress_time",
		"created_at":             "create_time",
		"fields":                 "attributes",
	}, key)
}

func protoEnumToLegacy(key, value string) string {
	if key != "phase" && key != "restart_policy" && key != "kind" {
		return value
	}
	for _, prefix := range []string{"NODE_PHASE_", "TASK_PHASE_", "RESTART_POLICY_", "PROBE_KIND_"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			if value == "UNSPECIFIED" {
				return ""
			}
			if key == "restart_policy" && value == "ON_FAILURE" {
				return "OnFailure"
			}
			if key == "restart_policy" {
				return strings.ToUpper(value[:1]) + strings.ToLower(value[1:])
			}
			if key == "kind" {
				return strings.ToLower(value)
			}
			return value
		}
	}
	return value
}

func legacyEnumToProto(key, value string) string {
	switch key {
	case "phase":
		if value == "" {
			return "TASK_PHASE_UNSPECIFIED"
		}
		if value == "JOINING" || value == "READY" || value == "SUSPECT" || value == "UNREACHABLE" || value == "DRAINING" || value == "REMOVED" {
			return "NODE_PHASE_" + value
		}
		return "TASK_PHASE_" + value
	case "restart_policy":
		if value == "" {
			return "RESTART_POLICY_UNSPECIFIED"
		}
		return "RESTART_POLICY_" + strings.ToUpper(strings.ReplaceAll(value, "Failure", "_FAILURE"))
	case "kind":
		if value == "" {
			return "PROBE_KIND_UNSPECIFIED"
		}
		return "PROBE_KIND_" + strings.ToUpper(value)
	default:
		return value
	}
}

func isDurationKey(key string) bool {
	return key == "initial_delay" || key == "period" || key == "timeout" || key == "progress_deadline"
}

func isSigned64Key(key string) bool {
	return key == "revision" || key == "generation" || key == "cpu_milli" || key == "memory_bytes" || key == "assignment_generation" || key == "observed_generation"
}

func isUnsigned64Key(key string) bool {
	return key == "storage_total_bytes" || key == "storage_available_bytes"
}

func mapLookup(mapping map[string]string, key string) string {
	if value := mapping[key]; value != "" {
		return value
	}
	return key
}
