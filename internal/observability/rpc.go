package observability

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/santinomarial/glider/internal/transport"
)

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type rpcKey struct{ method, code string }
type latencyState struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}
type RPCMetrics struct {
	mu                sync.Mutex
	requests          map[rpcKey]uint64
	latency           map[string]*latencyState
	inFlight          atomic.Int64
	leader            atomic.Int64
	leadershipChanges atomic.Uint64
	snapshotFailures  atomic.Uint64
	logMu             sync.Mutex
	log               io.Writer
	now               func() time.Time
}

func NewRPCMetrics(log io.Writer) *RPCMetrics {
	return &RPCMetrics{requests: map[rpcKey]uint64{}, latency: map[string]*latencyState{}, log: log, now: time.Now}
}

func (m *RPCMetrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := m.now()
		requestID := requestID(ctx)
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
		m.inFlight.Add(1)
		response, err := handler(ctx, request)
		m.inFlight.Add(-1)
		duration := m.now().Sub(started)
		code := status.Code(err).String()
		m.observe(info.FullMethod, code, duration)
		principal, _ := transport.PrincipalFromContext(ctx)
		m.writeLog(map[string]any{"time": m.now().UTC().Format(time.RFC3339Nano), "level": "info", "component": "grpc", "request_id": requestID, "principal": principal.Name, "method": info.FullMethod, "code": code, "duration_ms": float64(duration.Microseconds()) / 1000})
		return response, err
	}
}

func (m *RPCMetrics) observe(method, code string, duration time.Duration) {
	seconds := duration.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[rpcKey{method: method, code: code}]++
	state := m.latency[method]
	if state == nil {
		state = &latencyState{Buckets: make([]uint64, len(latencyBuckets))}
		m.latency[method] = state
	}
	state.Count++
	state.Sum += seconds
	for i, bound := range latencyBuckets {
		if seconds <= bound {
			state.Buckets[i]++
		}
	}
}

func (m *RPCMetrics) SetLeader(value bool) {
	if value {
		if m.leader.Swap(1) == 0 {
			m.leadershipChanges.Add(1)
		}
		return
	}
	m.leader.Store(0)
}

func (m *RPCMetrics) RecordSnapshotFailure() { m.snapshotFailures.Add(1) }

func (m *RPCMetrics) writeLog(value map[string]any) {
	if m.log == nil {
		return
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	_ = json.NewEncoder(m.log).Encode(value)
}

func (m *RPCMetrics) WritePrometheus(w io.Writer) {
	m.mu.Lock()
	requests := make(map[rpcKey]uint64, len(m.requests))
	for key, value := range m.requests {
		requests[key] = value
	}
	latency := make(map[string]latencyState, len(m.latency))
	for method, state := range m.latency {
		latency[method] = latencyState{Count: state.Count, Sum: state.Sum, Buckets: append([]uint64(nil), state.Buckets...)}
	}
	m.mu.Unlock()

	_, _ = io.WriteString(w, "# HELP glider_api_requests_total Completed gRPC requests by method and status code.\n# TYPE glider_api_requests_total counter\n")
	for key, value := range requests {
		_, _ = io.WriteString(w, "glider_api_requests_total{method="+strconv.Quote(key.method)+",code="+strconv.Quote(key.code)+"} "+strconv.FormatUint(value, 10)+"\n")
	}
	_, _ = io.WriteString(w, "# HELP glider_api_request_duration_seconds gRPC request latency.\n# TYPE glider_api_request_duration_seconds histogram\n")
	for method, state := range latency {
		for i, bound := range latencyBuckets {
			_, _ = io.WriteString(w, "glider_api_request_duration_seconds_bucket{method="+strconv.Quote(method)+",le="+strconv.Quote(strconv.FormatFloat(bound, 'g', -1, 64))+"} "+strconv.FormatUint(state.Buckets[i], 10)+"\n")
		}
		_, _ = io.WriteString(w, "glider_api_request_duration_seconds_bucket{method="+strconv.Quote(method)+",le=\"+Inf\"} "+strconv.FormatUint(state.Count, 10)+"\n")
		_, _ = io.WriteString(w, "glider_api_request_duration_seconds_sum{method="+strconv.Quote(method)+"} "+strconv.FormatFloat(state.Sum, 'g', -1, 64)+"\n")
		_, _ = io.WriteString(w, "glider_api_request_duration_seconds_count{method="+strconv.Quote(method)+"} "+strconv.FormatUint(state.Count, 10)+"\n")
	}
	_, _ = io.WriteString(w, "# HELP glider_api_in_flight Current gRPC requests.\n# TYPE glider_api_in_flight gauge\nglider_api_in_flight "+strconv.FormatInt(m.inFlight.Load(), 10)+"\n")
	_, _ = io.WriteString(w, "# HELP glider_controller_leader Whether this replica owns controller authority.\n# TYPE glider_controller_leader gauge\nglider_controller_leader "+strconv.FormatInt(m.leader.Load(), 10)+"\n")
	_, _ = io.WriteString(w, "# HELP glider_controller_leadership_changes_total Controller leadership acquisitions.\n# TYPE glider_controller_leadership_changes_total counter\nglider_controller_leadership_changes_total "+strconv.FormatUint(m.leadershipChanges.Load(), 10)+"\n")
	_, _ = io.WriteString(w, "# HELP glider_metrics_snapshot_failures_total Authoritative metrics snapshot failures.\n# TYPE glider_metrics_snapshot_failures_total counter\nglider_metrics_snapshot_failures_total "+strconv.FormatUint(m.snapshotFailures.Load(), 10)+"\n")
}

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func requestID(ctx context.Context) string {
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		values := incoming.Get("x-request-id")
		if len(values) == 1 && validRequestID.MatchString(values[0]) {
			return values[0]
		}
	}
	return uuid.NewString()
}
