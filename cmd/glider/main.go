package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"os"
	"strconv"
	"strings"
	"time"
)

const service = "glider.v1.ControlPlane"

type client struct{ conn *grpc.ClientConn }

func (c client) call(ctx context.Context, method string, input any) (map[string]any, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err = json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	in, _ := structpb.NewStruct(object)
	out := new(structpb.Struct)
	err = c.conn.Invoke(ctx, "/"+service+"/"+method, in, out)
	if err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}
func main() {
	endpoint := flag.String("endpoint", env("GLIDER_ENDPOINT", "127.0.0.1:8443"), "control-plane address")
	timeout := flag.Duration("timeout", 15*time.Second, "request timeout")
	tlsCert := flag.String("tls-cert", env("GLIDER_TLS_CERT", ""), "client TLS certificate")
	tlsKey := flag.String("tls-key", env("GLIDER_TLS_KEY", ""), "client TLS private key")
	caFile := flag.String("ca", env("GLIDER_CA", ""), "control-plane CA certificate")
	serverName := flag.String("tls-server-name", env("GLIDER_TLS_SERVER_NAME", ""), "expected control-plane certificate name")
	insecureDevelopment := flag.Bool("insecure-development", false, "disable TLS verification (development only)")
	flag.Parse()
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
	c := client{conn}
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
		return printCall(ctx, c, "PutTask", api.Task{Metadata: api.Metadata{ID: *id}, Spec: api.TaskSpec{Image: *image, Command: fs.Args()}, Status: api.TaskStatus{Phase: api.TaskPending}})
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
				return printCall(ctx, c, "PutWorkload", w)
			}
		}
		return errors.New("workload not found")
	case "nodes":
		return list(ctx, c, "ListNodes")
	case "ps":
		return list(ctx, c, "ListTasks")
	case "events":
		return list(ctx, c, "ListEvents")
	case "inspect":
		if len(args) != 3 {
			return errors.New("usage: glider inspect task|workload|service ID")
		}
		return inspect(ctx, c, args[1], args[2])
	case "logs", "exec", "stats":
		return fmt.Errorf("%s requires the node streaming API, which is not available in this release", args[0])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
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
func usage() {
	fmt.Fprintln(os.Stderr, "usage: glider [global flags] run|deploy|scale|nodes|ps|inspect|logs|exec|stats|events")
	os.Exit(2)
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "glider:", err); os.Exit(1) }
