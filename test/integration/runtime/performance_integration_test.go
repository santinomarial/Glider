//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type runtimePerformanceReport struct {
	Schema        string `json:"schema"`
	SourceCommit  string `json:"source_commit"`
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	Kernel        string `json:"kernel"`
	GOARCH        string `json:"goarch"`
	Iterations    int    `json:"iterations"`
	OperationsSec int64  `json:"operations_per_second"`
	P50NS         int64  `json:"p50_ns"`
	P95NS         int64  `json:"p95_ns"`
	P99NS         int64  `json:"p99_ns"`
	P99LimitNS    int64  `json:"p99_limit_ns"`
}

func TestRuntimeLifecyclePerformance(t *testing.T) {
	if os.Getenv("GLIDER_RUNTIME_PERFORMANCE") != "1" {
		t.Skip("set GLIDER_RUNTIME_PERFORMANCE=1 to run")
	}
	requireRoot(t)
	iterations := envInt(t, "GLIDER_RUNTIME_PERFORMANCE_ITERATIONS", 20, 5, 1000)
	p99Limit := envDuration(t, "GLIDER_RUNTIME_LIFECYCLE_P99_MAX", 500*time.Millisecond)
	glider, helper := buildBinaries(t)
	rootfs := newRootFS(t, helper)
	samples := make([]int64, 0, iterations)
	started := time.Now()
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		begin := time.Now()
		result, err := runGlider(t, ctx, glider, t.TempDir(), rootfs, []string{"--no-network"}, "/bin/glider-test-helper", "exit", "0")
		samples = append(samples, time.Since(begin).Nanoseconds())
		cancel()
		if err != nil {
			t.Fatalf("iteration %d lifecycle: %v", i+1, err)
		}
		if result.exitCode != 0 {
			t.Fatalf("iteration %d exit=%d stderr=%s", i+1, result.exitCode, result.stderr)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	report := runtimePerformanceReport{
		Schema:        "glider.performance/v1",
		SourceCommit:  os.Getenv("GLIDER_SOURCE_COMMIT"),
		Name:          "runtime_warm_rootfs_full_lifecycle",
		Mode:          "real_kernel_single_host",
		Kernel:        kernelRelease(),
		GOARCH:        runtime.GOARCH,
		Iterations:    iterations,
		OperationsSec: int64(float64(iterations) / time.Since(started).Seconds()),
		P50NS:         nearestRank(samples, 50),
		P95NS:         nearestRank(samples, 95),
		P99NS:         nearestRank(samples, 99),
		P99LimitNS:    p99Limit.Nanoseconds(),
	}
	if report.SourceCommit == "" {
		t.Fatal("GLIDER_SOURCE_COMMIT is required for auditable performance evidence")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("GLIDER_RUNTIME_PERFORMANCE %s\n", encoded)
	if report.P99NS > report.P99LimitNS {
		t.Fatalf("warm-rootfs full lifecycle p99 %s exceeds production envelope %s", time.Duration(report.P99NS), p99Limit)
	}
}

func nearestRank(sorted []int64, percentile int) int64 {
	return sorted[((percentile*len(sorted)+99)/100)-1]
}

func envInt(t *testing.T, name string, fallback, min, max int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		t.Fatalf("%s must be an integer from %d through %d", name, min, max)
	}
	return parsed
}

func envDuration(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("%s must be a positive Go duration", name)
	}
	return parsed
}

func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}
