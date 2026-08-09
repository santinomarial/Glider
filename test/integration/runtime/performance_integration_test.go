//go:build linux

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
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

func TestColdImageLifecyclePerformance(t *testing.T) {
	if os.Getenv("GLIDER_RUNTIME_PERFORMANCE") != "1" {
		t.Skip("set GLIDER_RUNTIME_PERFORMANCE=1 to run")
	}
	requireRoot(t)
	iterations := envInt(t, "GLIDER_COLD_IMAGE_PERFORMANCE_ITERATIONS", 10, 5, 100)
	p99Limit := envDuration(t, "GLIDER_COLD_IMAGE_LIFECYCLE_P99_MAX", 2*time.Second)
	glider, helper := buildBinaries(t)
	imageRef, closeRegistry := serveTestImage(t, helper)
	defer closeRegistry()
	samples := make([]int64, 0, iterations)
	started := time.Now()
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		args := []string{"run", "--image", imageRef, "--insecure-registry", "--data-dir", t.TempDir(), "--state-dir", t.TempDir(), "--no-network", "--", "/bin/glider-test-helper", "exit", "0"}
		cmd := exec.CommandContext(ctx, glider, args...)
		begin := time.Now()
		output, err := cmd.CombinedOutput()
		samples = append(samples, time.Since(begin).Nanoseconds())
		cancel()
		if err != nil {
			t.Fatalf("iteration %d cold image lifecycle: %v: %s", i+1, err, output)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	report := runtimePerformanceReport{Schema: "glider.performance/v1", SourceCommit: os.Getenv("GLIDER_SOURCE_COMMIT"), Name: "runtime_cold_local_registry_image_full_lifecycle", Mode: "real_kernel_single_host", Kernel: kernelRelease(), GOARCH: runtime.GOARCH, Iterations: iterations, OperationsSec: int64(float64(iterations) / time.Since(started).Seconds()), P50NS: nearestRank(samples, 50), P95NS: nearestRank(samples, 95), P99NS: nearestRank(samples, 99), P99LimitNS: p99Limit.Nanoseconds()}
	if report.SourceCommit == "" {
		t.Fatal("GLIDER_SOURCE_COMMIT is required for auditable performance evidence")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("GLIDER_RUNTIME_PERFORMANCE %s\n", encoded)
	if report.P99NS > report.P99LimitNS {
		t.Fatalf("cold local-registry image full lifecycle p99 %s exceeds production envelope %s", time.Duration(report.P99NS), p99Limit)
	}
}

func serveTestImage(t *testing.T, helper string) (string, func()) {
	t.Helper()
	binary, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	var uncompressed bytes.Buffer
	tarWriter := tar.NewWriter(&uncompressed)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "bin/glider-test-helper", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	var layer bytes.Buffer
	gzipWriter := gzip.NewWriter(&layer)
	if _, err := io.Copy(gzipWriter, bytes.NewReader(uncompressed.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	layerBytes := layer.Bytes()
	configBytes := mustMarshalJSON(t, v1.Image{
		Platform: v1.Platform{OS: "linux", Architecture: runtime.GOARCH},
		RootFS:   v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromBytes(uncompressed.Bytes())}},
	})
	configDesc := ociDescriptor(configBytes, v1.MediaTypeImageConfig)
	layerDesc := ociDescriptor(layerBytes, v1.MediaTypeImageLayerGzip)
	manifestBytes := mustMarshalJSON(t, v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest, Config: configDesc, Layers: []v1.Descriptor{layerDesc}})
	manifestDigest := digest.FromBytes(manifestBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch r.URL.Path {
		case "/v2/glider/performance/manifests/latest":
			w.Header().Set("Content-Type", v1.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
			body = manifestBytes
		case "/v2/glider/performance/blobs/" + configDesc.Digest.String():
			body = configBytes
		case "/v2/glider/performance/blobs/" + layerDesc.Digest.String():
			body = layerBytes
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.Copy(w, bytes.NewReader(body))
	}))
	return strings.TrimPrefix(server.URL, "http://") + "/glider/performance:latest", server.Close
}

func ociDescriptor(data []byte, mediaType string) v1.Descriptor {
	return v1.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
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
