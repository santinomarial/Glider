// Package apiv2 translates between Glider's internal JSON resource model and
// the typed v2 protobuf model while v1 compatibility remains supported.
package apiv2

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ToLegacy converts a typed protobuf message to the internal JSON shape used
// by the authoritative store and legacy API implementation.
func ToLegacy(message proto.Message) (map[string]any, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return nil, fmt.Errorf("protobuf message is required")
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return protoToLegacyMap(object)
}

// FromLegacy converts an internal JSON-compatible value into a typed
// protobuf message. Unknown fields fail closed.
func FromLegacy(value any, destination proto.Message) error {
	if destination == nil || !destination.ProtoReflect().IsValid() {
		return fmt.Errorf("protobuf destination is required")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	data, err = json.Marshal(legacyToProtoMap(object))
	if err != nil {
		return err
	}
	return (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, destination)
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
