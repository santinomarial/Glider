package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/santinomarial/glider/internal/api"
)

type SecretResolver interface {
	Resolve(context.Context, api.Assignment) ([]string, error)
}

type assignmentSecretsClient interface {
	GetAssignmentSecrets(context.Context, *gliderv2.GetAssignmentSecretsRequest, ...grpc.CallOption) (*gliderv2.GetAssignmentSecretsResponse, error)
}

type ControlPlaneSecretResolver struct {
	conn   *grpc.ClientConn
	client assignmentSecretsClient
}

func NewControlPlaneSecretResolver(endpoint string, transport credentials.TransportCredentials) (*ControlPlaneSecretResolver, error) {
	if endpoint == "" || transport == nil {
		return nil, errors.New("secret resolver requires control-plane endpoint and TLS credentials")
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, err
	}
	return &ControlPlaneSecretResolver{conn: conn, client: gliderv2.NewControlPlaneServiceClient(conn)}, nil
}

func (r *ControlPlaneSecretResolver) Close() error { return r.conn.Close() }

func (r *ControlPlaneSecretResolver) Resolve(ctx context.Context, assignment api.Assignment) ([]string, error) {
	if len(assignment.Secrets) == 0 {
		return nil, nil
	}
	out, err := r.client.GetAssignmentSecrets(ctx, &gliderv2.GetAssignmentSecretsRequest{TaskId: assignment.TaskID, Generation: assignment.Generation})
	if err != nil {
		return nil, err
	}
	if out.GetTaskId() != assignment.TaskID || out.GetGeneration() != assignment.Generation {
		return nil, errors.New("secret delivery response does not match the requested assignment generation")
	}
	return decodeSecretValues(out.GetEnvironment(), assignment)
}

func decodeSecretValues(environment map[string][]byte, assignment api.Assignment) ([]string, error) {
	if len(environment) != len(assignment.Secrets) {
		return nil, errors.New("invalid secret delivery response")
	}
	allowed := make(map[string]bool, len(assignment.Secrets))
	for _, ref := range assignment.Secrets {
		allowed[ref.Env] = true
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if !allowed[key] {
			return nil, fmt.Errorf("secret delivery returned unrequested environment %s", key)
		}
		data := environment[key]
		if strings.ContainsRune(string(data), '\x00') {
			return nil, fmt.Errorf("secret value for %s cannot be represented in an environment", key)
		}
		values = append(values, key+"="+string(data))
	}
	return values, nil
}
