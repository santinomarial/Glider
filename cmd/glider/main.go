package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gliderv2 "github.com/santinomarial/glider/api/gen/glider/v2"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/apiv2"
	"github.com/santinomarial/glider/internal/transport"
	"github.com/santinomarial/glider/internal/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type client struct {
	conn         *grpc.ClientConn
	control      gliderv2.ControlPlaneServiceClient
	transport    credentials.TransportCredentials
	nodeEndpoint string
}

func (c client) call(ctx context.Context, method string, input any) (map[string]any, error) {
	var response proto.Message
	var err error
	switch method {
	case "PutTask":
		request := new(gliderv2.PutTaskRequest)
		if err = controlRequest(input, "task", request); err == nil {
			response, err = c.control.PutTask(ctx, request)
		}
	case "GetTask":
		request := new(gliderv2.GetTaskRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.GetTask(ctx, request)
		}
	case "DeleteTask":
		request := new(gliderv2.DeleteTaskRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.DeleteTask(ctx, request)
		}
	case "PutWorkload":
		request := new(gliderv2.PutWorkloadRequest)
		if err = controlRequest(input, "workload", request); err == nil {
			response, err = c.control.PutWorkload(ctx, request)
		}
	case "DeleteWorkload":
		request := new(gliderv2.DeleteWorkloadRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.DeleteWorkload(ctx, request)
		}
	case "ListWorkloads":
		response, err = c.control.ListWorkloads(ctx, &gliderv2.ListWorkloadsRequest{})
	case "DeleteService":
		request := new(gliderv2.DeleteServiceRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.DeleteService(ctx, request)
		}
	case "ListServices":
		response, err = c.control.ListServices(ctx, &gliderv2.ListServicesRequest{})
	case "ListNodes":
		response, err = c.control.ListNodes(ctx, &gliderv2.ListNodesRequest{})
	case "DrainNode":
		request := new(gliderv2.DrainNodeRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.DrainNode(ctx, request)
		}
	case "RemoveNode":
		request := new(gliderv2.RemoveNodeRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.RemoveNode(ctx, request)
		}
	case "ListTasks":
		response, err = c.control.ListTasks(ctx, &gliderv2.ListTasksRequest{})
	case "ListEvents":
		response, err = c.control.ListEvents(ctx, &gliderv2.ListEventsRequest{})
	case "PutSecret":
		request := new(gliderv2.PutSecretRequest)
		if err = controlRequest(input, "secret", request); err == nil {
			response, err = c.control.PutSecret(ctx, request)
		}
	case "ListSecrets":
		response, err = c.control.ListSecrets(ctx, &gliderv2.ListSecretsRequest{})
	case "DeleteSecret":
		request := new(gliderv2.DeleteSecretRequest)
		if err = controlRequest(input, "", request); err == nil {
			response, err = c.control.DeleteSecret(ctx, request)
		}
	default:
		return nil, fmt.Errorf("unsupported control-plane method %q", method)
	}
	if err != nil {
		return nil, err
	}
	result, err := apiv2.ToLegacy(response)
	if err != nil {
		return nil, err
	}
	if wrapper := responseWrapper(method); wrapper != "" {
		resource, ok := result[wrapper].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("typed %s response has no %s", method, wrapper)
		}
		return resource, nil
	}
	return result, nil
}

func controlRequest(input any, wrapper string, destination proto.Message) error {
	if wrapper != "" {
		input = map[string]any{wrapper: input}
	}
	return apiv2.FromLegacy(input, destination)
}

func responseWrapper(method string) string {
	return map[string]string{
		"PutTask":        "task",
		"GetTask":        "task",
		"DeleteTask":     "result",
		"PutWorkload":    "workload",
		"DeleteWorkload": "workload",
		"DeleteService":  "result",
		"DrainNode":      "node",
		"RemoveNode":     "result",
		"PutSecret":      "secret",
		"DeleteSecret":   "result",
	}[method]
}
func main() {
	endpoint := flag.String("endpoint", env("GLIDER_ENDPOINT", "127.0.0.1:8443"), "control-plane address")
	timeout := flag.Duration("timeout", 15*time.Second, "request timeout")
	tlsCert := flag.String("tls-cert", env("GLIDER_TLS_CERT", ""), "client TLS certificate")
	tlsKey := flag.String("tls-key", env("GLIDER_TLS_KEY", ""), "client TLS private key")
	caFile := flag.String("ca", env("GLIDER_CA", ""), "control-plane CA certificate")
	serverName := flag.String("tls-server-name", env("GLIDER_TLS_SERVER_NAME", ""), "expected control-plane certificate name")
	nodeEndpoint := flag.String("node-endpoint", "", "override node operations address")
	insecureDevelopment := flag.Bool("insecure-development", false, "disable TLS verification (development only)")
	flag.Parse()
	if flag.NArg() == 1 && flag.Arg(0) == "version" {
		fmt.Println(version.Version)
		return
	}
	if flag.NArg() == 0 {
		usage()
	}
	var transportCredentials credentials.TransportCredentials
	var err error
	if *insecureDevelopment {
		transportCredentials = insecure.NewCredentials()
	} else {
		transportCredentials, err = transport.ClientCredentials(*tlsCert, *tlsKey, *caFile, *serverName)
		if err != nil {
			fatal(err)
		}
	}
	conn, err := grpc.NewClient(*endpoint, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c := client{conn: conn, control: gliderv2.NewControlPlaneServiceClient(conn), transport: transportCredentials, nodeEndpoint: *nodeEndpoint}
	if err := run(ctx, c, flag.Args()); err != nil {
		fatal(err)
	}
}
func run(ctx context.Context, c client, args []string) error {
	switch args[0] {
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		id := fs.String("id", "", "task ID")
		image := fs.String("image", "", "OCI image")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *image == "" {
			return errors.New("run requires --id and --image")
		}
		return printCall(ctx, c, "PutTask", api.Task{Metadata: api.Metadata{ID: *id, IdempotencyKey: newIdempotencyKey()}, Spec: api.TaskSpec{Image: *image, Command: fs.Args()}, Status: api.TaskStatus{Phase: api.TaskPending}})
	case "stop":
		if len(args) != 2 {
			return errors.New("usage: glider stop TASK")
		}
		task, err := c.call(ctx, "GetTask", map[string]any{"id": args[1]})
		if err != nil {
			return err
		}
		metadata, _ := task["metadata"].(map[string]any)
		revision, _ := metadata["revision"].(float64)
		if revision <= 0 {
			return errors.New("task response has no revision")
		}
		return printCall(ctx, c, "DeleteTask", map[string]any{"id": args[1], "revision": revision})
	case "deploy":
		if len(args) != 2 {
			return errors.New("usage: glider deploy FILE.json")
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		var workload api.Workload
		if err = json.Unmarshal(data, &workload); err != nil {
			return err
		}
		workload.Metadata.IdempotencyKey = newIdempotencyKey()
		return printCall(ctx, c, "PutWorkload", workload)
	case "scale":
		if len(args) != 3 {
			return errors.New("usage: glider scale WORKLOAD REPLICAS")
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 0 {
			return errors.New("replicas must be a non-negative integer")
		}
		result, err := c.call(ctx, "ListWorkloads", map[string]any{})
		if err != nil {
			return err
		}
		var workloads []api.Workload
		if err = items(result, &workloads); err != nil {
			return err
		}
		for _, w := range workloads {
			if w.Metadata.ID == args[1] || w.Metadata.Name == args[1] {
				w.Spec.Replicas = n
				w.Metadata.IdempotencyKey = newIdempotencyKey()
				return printCall(ctx, c, "PutWorkload", w)
			}
		}
		return errors.New("workload not found")
	case "delete":
		if len(args) != 3 || (args[1] != "workload" && args[1] != "service") {
			return errors.New("usage: glider delete workload|service ID")
		}
		kind := args[1]
		method := "ListWorkloads"
		deleteMethod := "DeleteWorkload"
		if kind == "service" {
			method, deleteMethod = "ListServices", "DeleteService"
		}
		result, err := c.call(ctx, method, map[string]any{})
		if err != nil {
			return err
		}
		var resources []struct {
			Metadata api.Metadata `json:"metadata"`
		}
		if err := items(result, &resources); err != nil {
			return err
		}
		for _, resource := range resources {
			if resource.Metadata.ID == args[2] || resource.Metadata.Name == args[2] {
				return printCall(ctx, c, deleteMethod, map[string]any{"id": resource.Metadata.ID, "revision": resource.Metadata.Revision})
			}
		}
		return fmt.Errorf("%s not found", kind)
	case "nodes":
		return list(ctx, c, "ListNodes")
	case "drain":
		if len(args) != 2 {
			return errors.New("usage: glider drain NODE")
		}
		result, err := c.call(ctx, "ListNodes", map[string]any{})
		if err != nil {
			return err
		}
		var nodes []api.Node
		if err := items(result, &nodes); err != nil {
			return err
		}
		for _, node := range nodes {
			if node.Metadata.ID == args[1] || node.Metadata.Name == args[1] {
				return printCall(ctx, c, "DrainNode", map[string]any{"id": node.Metadata.ID, "revision": node.Metadata.Revision})
			}
		}
		return errors.New("node not found")
	case "remove-node":
		if len(args) != 2 {
			return errors.New("usage: glider remove-node NODE")
		}
		result, err := c.call(ctx, "ListNodes", map[string]any{})
		if err != nil {
			return err
		}
		var nodes []api.Node
		if err := items(result, &nodes); err != nil {
			return err
		}
		for _, node := range nodes {
			if node.Metadata.ID == args[1] || node.Metadata.Name == args[1] {
				return printCall(ctx, c, "RemoveNode", map[string]any{"id": node.Metadata.ID, "revision": node.Metadata.Revision})
			}
		}
		return errors.New("node not found")
	case "ps":
		return list(ctx, c, "ListTasks")
	case "events":
		return list(ctx, c, "ListEvents")
	case "secret":
		return runSecret(ctx, c, args[1:])
	case "inspect":
		if len(args) != 3 {
			return errors.New("usage: glider inspect task|workload|service ID")
		}
		return inspect(ctx, c, args[1], args[2])
	case "logs":
		if len(args) != 2 {
			return errors.New("usage: glider logs TASK")
		}
		result, err := c.nodeCall(ctx, "GetLogs", args[1], map[string]any{"tail_bytes": float64(64 << 10)})
		if err != nil {
			return err
		}
		encoded, _ := result["data_base64"].(string)
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	case "stats":
		if len(args) != 2 {
			return errors.New("usage: glider stats TASK")
		}
		result, err := c.nodeCall(ctx, "GetStats", args[1], nil)
		if err != nil {
			return err
		}
		return pretty(result)
	case "exec":
		if len(args) < 3 {
			return errors.New("usage: glider exec TASK -- COMMAND [ARG...]")
		}
		command := args[2:]
		if command[0] == "--" {
			command = command[1:]
		}
		if len(command) == 0 {
			return errors.New("exec command is required")
		}
		values := make([]any, len(command))
		for i, value := range command {
			values[i] = value
		}
		result, err := c.nodeCall(ctx, "Exec", args[1], map[string]any{"command": values, "timeout_seconds": 30})
		if err != nil {
			return err
		}
		encoded, _ := result["data_base64"].(string)
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		_, _ = os.Stdout.Write(data)
		code, _ := result["exit_code"].(float64)
		if code != 0 {
			return fmt.Errorf("remote command exited with code %d", int(code))
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func runSecret(ctx context.Context, c client, args []string) error {
	if len(args) == 1 && args[0] == "list" {
		return list(ctx, c, "ListSecrets")
	}
	if len(args) == 2 && args[0] == "delete" {
		result, err := c.call(ctx, "ListSecrets", map[string]any{})
		if err != nil {
			return err
		}
		var values []api.Secret
		if err := items(result, &values); err != nil {
			return err
		}
		for _, value := range values {
			if value.Metadata.ID == args[1] || value.Metadata.Name == args[1] {
				return printCall(ctx, c, "DeleteSecret", map[string]any{"id": value.Metadata.ID, "revision": value.Metadata.Revision})
			}
		}
		return errors.New("secret not found")
	}
	if len(args) >= 3 && args[0] == "put" {
		value := api.Secret{Metadata: api.Metadata{ID: args[1], IdempotencyKey: newIdempotencyKey()}, Data: make(map[string][]byte)}
		for _, source := range args[2:] {
			key, path, ok := strings.Cut(source, "=")
			if !ok || key == "" || path == "" {
				return errors.New("secret sources must use KEY=FILE")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read secret source %s: %w", path, err)
			}
			value.Data[key] = data
		}
		result, err := c.call(ctx, "ListSecrets", map[string]any{})
		if err != nil {
			return err
		}
		var existing []api.Secret
		if err := items(result, &existing); err != nil {
			return err
		}
		for _, item := range existing {
			if item.Metadata.ID == value.Metadata.ID {
				value.Metadata.Revision = item.Metadata.Revision
			}
		}
		return printCall(ctx, c, "PutSecret", value)
	}
	return errors.New("usage: glider secret put NAME KEY=FILE... | secret list | secret delete NAME")
}
func (c client) nodeCall(ctx context.Context, method, taskID string, extra map[string]any) (map[string]any, error) {
	task, err := c.call(ctx, "GetTask", map[string]any{"id": taskID})
	if err != nil {
		return nil, err
	}
	statusValue, _ := task["status"].(map[string]any)
	generation, _ := statusValue["assignment_generation"].(float64)
	nodeID, _ := statusValue["node_id"].(string)
	if generation <= 0 || nodeID == "" {
		return nil, errors.New("task has no active assignment")
	}
	endpoint := c.nodeEndpoint
	if endpoint == "" {
		nodes, err := c.call(ctx, "ListNodes", map[string]any{})
		if err != nil {
			return nil, err
		}
		values, _ := nodes["items"].([]any)
		for _, value := range values {
			node, _ := value.(map[string]any)
			metadata, _ := node["metadata"].(map[string]any)
			if metadata["id"] != nodeID {
				continue
			}
			spec, _ := node["spec"].(map[string]any)
			endpoint, _ = spec["operations_address"].(string)
		}
	}
	if endpoint == "" {
		return nil, errors.New("assigned node has no operations address")
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(c.transport.Clone()))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	input := map[string]any{"task_id": taskID, "generation": generation}
	for key, value := range extra {
		input[key] = value
	}
	data, _ := structpb.NewStruct(input)
	out := new(structpb.Struct)
	if err = conn.Invoke(ctx, "/glider.v1.NodeOperations/"+method, data, out); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
func inspect(ctx context.Context, c client, kind, id string) error {
	if kind == "task" {
		return printCall(ctx, c, "GetTask", map[string]any{"id": id})
	}
	methods := map[string]string{"workload": "ListWorkloads", "service": "ListServices"}
	method, ok := methods[kind]
	if !ok {
		return errors.New("kind must be task, workload, or service")
	}
	result, err := c.call(ctx, method, map[string]any{})
	if err != nil {
		return err
	}
	values, ok := result["items"].([]any)
	if !ok {
		return errors.New("invalid server response")
	}
	for _, v := range values {
		m, _ := v.(map[string]any)
		metadata, _ := m["metadata"].(map[string]any)
		if metadata["id"] == id || metadata["name"] == id {
			return pretty(m)
		}
	}
	return errors.New("resource not found")
}
func list(ctx context.Context, c client, method string) error {
	result, err := c.call(ctx, method, map[string]any{})
	if err != nil {
		return err
	}
	return pretty(result["items"])
}
func printCall(ctx context.Context, c client, method string, input any) error {
	result, err := c.call(ctx, method, input)
	if err != nil {
		return err
	}
	return pretty(result)
}
func items(value map[string]any, out any) error {
	data, err := json.Marshal(value["items"])
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func pretty(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}
func env(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}
func newIdempotencyKey() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		fatal(fmt.Errorf("generate idempotency key: %w", err))
	}
	return hex.EncodeToString(value[:])
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: glider [global flags] run|stop|deploy|scale|delete|nodes|drain|remove-node|ps|inspect|logs|exec|stats|events|secret")
	os.Exit(2)
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "glider:", err); os.Exit(1) }
