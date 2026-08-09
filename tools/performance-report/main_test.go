package main

import (
	"testing"
	"time"
)

func TestPercentileUsesNearestRankWithoutMutatingInput(t *testing.T) {
	values := []int64{100, 10, 50, 20, 30}
	if got := percentile(values, 50); got != 30 {
		t.Fatalf("p50=%d", got)
	}
	if got := percentile(values, 99); got != 100 {
		t.Fatalf("p99=%d", got)
	}
	if values[0] != 100 || values[1] != 10 {
		t.Fatalf("input mutated: %v", values)
	}
}

func TestValidateRejectsMissingAndRegressedScenarios(t *testing.T) {
	limits := map[string]time.Duration{"one": 100 * time.Nanosecond, "two": 200 * time.Nanosecond}
	if err := validate([]measurement{{Name: "one", P99NS: 50}}, limits); err == nil {
		t.Fatal("missing performance scenario accepted")
	}
	if err := validate([]measurement{{Name: "one", P99NS: 101}, {Name: "two", P99NS: 100}}, limits); err == nil {
		t.Fatal("p99 regression accepted")
	}
	if err := validate([]measurement{{Name: "one", P99NS: 100}, {Name: "two", P99NS: 200}}, limits); err != nil {
		t.Fatalf("limits rejected: %v", err)
	}
}

func TestMeasurementReportsPercentilesAndThroughput(t *testing.T) {
	result := measure("test", "simulated", 100, func() {})
	if result.Iterations != 100 || result.OperationsSec <= 0 || result.P50NS < 0 || result.P95NS < result.P50NS || result.P99NS < result.P95NS {
		t.Fatalf("measurement=%+v", result)
	}
}
