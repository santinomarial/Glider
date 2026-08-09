package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/santinomarial/glider/internal/api"
)

type SecretResolver interface {
	Resolve(context.Context, api.Assignment) ([]string, error)
}

type ControlPlaneSecretResolver struct{ conn *grpc.ClientConn }

func NewControlPlaneSecretResolver(endpoint string, transport credentials.TransportCredentials) (*ControlPlaneSecretResolver, error) {
	if endpoint == "" || transport == nil {
		return nil, errors.New("secret resolver requires control-plane endpoint and TLS credentials")
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, err
	}
	return &ControlPlaneSecretResolver{conn: conn}, nil
}

func (r *ControlPlaneSecretResolver) Close() error { return r.conn.Close() }

func (r *ControlPlaneSecretResolver) Resolve(ctx context.Context, assignment api.Assignment) ([]string, error) {
	if len(assignment.Secrets) == 0 {
		return nil, nil
	}
	in, _ := structpb.NewStruct(map[string]any{"task_id": assignment.TaskID, "generation": assignment.Generation})
	out := new(structpb.Struct)
	if err := r.conn.Invoke(ctx, "/glider.v1.ControlPlane/GetAssignmentSecrets", in, out); err != nil {
		return nil, err
	}
	encoded, ok := out.AsMap()["values"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid secret delivery response")
	}
	return decodeSecretValues(encoded, assignment)
}

func decodeSecretValues(encoded map[string]any, assignment api.Assignment) ([]string, error) {
	if len(encoded) != len(assignment.Secrets) {
		return nil, errors.New("invalid secret delivery response")
	}
	allowed := make(map[string]bool, len(assignment.Secrets))
	for _, ref := range assignment.Secrets {
		allowed[ref.Env] = true
	}
	keys := make([]string, 0, len(encoded))
	for key := range encoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if !allowed[key] {
			return nil, fmt.Errorf("secret delivery returned unrequested environment %s", key)
		}
		value, ok := encoded[key].(string)
		if !ok {
			return nil, fmt.Errorf("invalid secret value for %s", key)
		}
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil || strings.ContainsRune(string(data), '\x00') {
			return nil, fmt.Errorf("secret value for %s cannot be represented in an environment", key)
		}
		values = append(values, key+"="+string(data))
	}
	return values, nil
}
