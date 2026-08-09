package observability

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/santinomarial/glider/internal/api"
)

func TestRPCMetricsCorrelateLogsAndExposeFailures(t *testing.T) {
	var logs bytes.Buffer
	metrics := NewRPCMetrics(&logs)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "request-42"))
	_, err := metrics.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/glider.v1.ControlPlane/CreateTask"}, func(context.Context, any) (any, error) {
		return nil, status.Error(codes.InvalidArgument, "bad task")
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	var output bytes.Buffer
	metrics.WritePrometheus(&output)
	for _, want := range []string{"glider_api_requests_total{method=\"/glider.v1.ControlPlane/CreateTask\",code=\"InvalidArgument\"} 1", "glider_api_request_duration_seconds_count{method=\"/glider.v1.ControlPlane/CreateTask\"} 1", "glider_api_in_flight 0"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, output.String())
		}
	}
	if !strings.Contains(logs.String(), `"request_id":"request-42"`) || !strings.Contains(logs.String(), `"code":"InvalidArgument"`) {
		t.Fatalf("uncorrelated access log: %s", logs.String())
	}
}

func TestMetricsSnapshotFailureIsCounted(t *testing.T) {
	metrics := NewRPCMetrics(nil)
	recorder := httptest.NewRecorder()
	NewMetricsHandler(failingSnapshot{}, metrics).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 503 {
		t.Fatalf("status=%d", recorder.Code)
	}
	var output bytes.Buffer
	metrics.WritePrometheus(&output)
	if !strings.Contains(output.String(), "glider_metrics_snapshot_failures_total 1") {
		t.Fatal(output.String())
	}
}

type failingSnapshot struct{ fake }

func (failingSnapshot) ListNodes(context.Context) ([]api.Node, error) {
	return nil, errors.New("etcd unavailable")
}
