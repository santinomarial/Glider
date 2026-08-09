package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/discovery"
	"github.com/santinomarial/glider/internal/scheduler"
)

type measurement struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	Iterations    int    `json:"iterations"`
	OperationsSec int64  `json:"operations_per_second"`
	P50NS         int64  `json:"p50_ns"`
	P95NS         int64  `json:"p95_ns"`
	P99NS         int64  `json:"p99_ns"`
}

type report struct {
	Schema      string        `json:"schema"`
	Commit      string        `json:"source_commit"`
	GeneratedAt time.Time     `json:"generated_at"`
	GoVersion   string        `json:"go_version"`
	GOOS        string        `json:"goos"`
	GOARCH      string        `json:"goarch"`
	CPUs        int           `json:"cpus"`
	Results     []measurement `json:"results"`
}

type serviceLister struct{ services []api.Service }

func (l serviceLister) ListServices(context.Context) ([]api.Service, error) { return l.services, nil }

func main() {
	iterations := flag.Int("iterations", 2000, "timed operations per scenario")
	commit := flag.String("commit", "unknown", "exact source commit")
	schedule100P99 := flag.Duration("scheduler-100-p99-max", 250*time.Microsecond, "maximum scheduler 100-node p99")
	schedule1000P99 := flag.Duration("scheduler-1000-p99-max", 750*time.Microsecond, "maximum scheduler 1000-node p99")
	discoveryP99 := flag.Duration("discovery-1000-p99-max", 500*time.Nanosecond, "maximum 1000-endpoint discovery p99")
	flag.Parse()
	if *iterations < 100 || *iterations > 1_000_000 || *commit == "" {
		fmt.Fprintln(os.Stderr, "iterations must be 100..1000000 and commit must be non-empty")
		os.Exit(2)
	}
	results := []measurement{
		measureSchedule("scheduler_100_nodes", 100, *iterations),
		measureSchedule("scheduler_1000_nodes", 1000, *iterations),
		measureDiscovery(*iterations),
	}
	value := report{Schema: "glider.performance/v1", Commit: *commit, GeneratedAt: time.Now().UTC(), GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CPUs: runtime.NumCPU(), Results: results}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	limits := map[string]time.Duration{"scheduler_100_nodes": *schedule100P99, "scheduler_1000_nodes": *schedule1000P99, "discovery_1000_endpoints": *discoveryP99}
	if err := validate(results, limits); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validate(results []measurement, limits map[string]time.Duration) error {
	seen := make(map[string]bool, len(results))
	for _, result := range results {
		limit, ok := limits[result.Name]
		if !ok || limit <= 0 {
			return fmt.Errorf("performance result %s has no positive p99 limit", result.Name)
		}
		seen[result.Name] = true
		if time.Duration(result.P99NS) > limit {
			return fmt.Errorf("%s p99 %s exceeds production limit %s", result.Name, time.Duration(result.P99NS), limit)
		}
	}
	if len(seen) != len(limits) {
		return fmt.Errorf("performance report covered %d of %d required scenarios", len(seen), len(limits))
	}
	return nil
}

func measureSchedule(name string, count, iterations int) measurement {
	nodes := make([]api.Node, count)
	for i := range nodes {
		nodes[i] = api.Node{Metadata: api.Metadata{ID: fmt.Sprintf("node-%04d", i)}, Spec: api.NodeSpec{Capacity: api.Resources{CPUMilli: 8000, MemoryBytes: 16 << 30}}, Status: api.NodeStatus{Phase: api.NodeReady}}
	}
	task := api.Task{Spec: api.TaskSpec{WorkloadID: "performance", Resources: api.Resources{CPUMilli: 100, MemoryBytes: 64 << 20}}}
	return measure(name, "simulated_control_plane", iterations, func() {
		if _, err := scheduler.Schedule(task, nodes, nil); err != nil {
			panic(err)
		}
	})
}

func measureDiscovery(iterations int) measurement {
	endpoints := make([]api.ServiceEndpoint, 1000)
	for i := range endpoints {
		endpoints[i] = api.ServiceEndpoint{Address: fmt.Sprintf("10.64.%d.%d", (i/250)+1, (i%250)+1)}
	}
	dns, err := discovery.NewDNS(serviceLister{services: []api.Service{{Metadata: api.Metadata{ID: "api"}, Status: api.ServiceStatus{ClusterIP: "10.96.0.42", Endpoints: endpoints}}}})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	return measure("discovery_1000_endpoints", "simulated_control_plane", iterations, func() {
		if _, err := dns.Lookup(ctx, "api.glider"); err != nil {
			panic(err)
		}
	})
}

func measure(name, mode string, iterations int, operation func()) measurement {
	for i := 0; i < 50; i++ {
		operation()
	}
	samples := make([]int64, iterations)
	started := time.Now()
	for i := range samples {
		before := time.Now()
		operation()
		samples[i] = time.Since(before).Nanoseconds()
	}
	elapsed := time.Since(started)
	return measurement{Name: name, Mode: mode, Iterations: iterations, OperationsSec: int64(float64(iterations) / elapsed.Seconds()), P50NS: percentile(samples, 50), P95NS: percentile(samples, 95), P99NS: percentile(samples, 99)}
}

func percentile(values []int64, percent int) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (len(ordered)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return ordered[index-1]
}
