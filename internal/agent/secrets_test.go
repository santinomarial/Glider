package agent

import (
	"context"
	"testing"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
	"google.golang.org/grpc"
)

func TestDecodeSecretValuesRequiresExactRequestedEnvironment(t *testing.T) {
	assignment := api.Assignment{Secrets: []api.SecretEnvRef{{SecretID: "database", Key: "password", Env: "PASSWORD"}}}
	values, err := decodeSecretValues(map[string][]byte{"PASSWORD": []byte("value")}, assignment)
	if err != nil || len(values) != 1 || values[0] != "PASSWORD=value" {
		t.Fatalf("values = %q, %v", values, err)
	}
	if _, err := decodeSecretValues(map[string][]byte{"OTHER": []byte("value")}, assignment); err == nil {
		t.Fatal("unrequested environment accepted")
	}
	if _, err := decodeSecretValues(map[string][]byte{"PASSWORD": {'a', 0, 'b'}}, assignment); err == nil {
		t.Fatal("NUL-containing environment value accepted")
	}
}

func TestControlPlaneSecretResolverUsesTypedGenerationFencedRequest(t *testing.T) {
	client := &fakeAssignmentSecretsClient{response: &gliderv2.GetAssignmentSecretsResponse{
		TaskId:      "task",
		Generation:  7,
		Environment: map[string][]byte{"PASSWORD": []byte("value")},
	}}
	resolver := &ControlPlaneSecretResolver{client: client}
	assignment := api.Assignment{TaskID: "task", Generation: 7, Secrets: []api.SecretEnvRef{{SecretID: "database", Key: "password", Env: "PASSWORD"}}}
	values, err := resolver.Resolve(context.Background(), assignment)
	if err != nil || len(values) != 1 || values[0] != "PASSWORD=value" {
		t.Fatalf("values = %q, %v", values, err)
	}
	if client.request.GetTaskId() != "task" || client.request.GetGeneration() != 7 {
		t.Fatalf("request = %+v", client.request)
	}

	client.response.Generation++
	if _, err := resolver.Resolve(context.Background(), assignment); err == nil {
		t.Fatal("mismatched response generation accepted")
	}
}

type fakeAssignmentSecretsClient struct {
	request  *gliderv2.GetAssignmentSecretsRequest
	response *gliderv2.GetAssignmentSecretsResponse
}

func (c *fakeAssignmentSecretsClient) GetAssignmentSecrets(_ context.Context, request *gliderv2.GetAssignmentSecretsRequest, _ ...grpc.CallOption) (*gliderv2.GetAssignmentSecretsResponse, error) {
	c.request = request
	return c.response, nil
}
