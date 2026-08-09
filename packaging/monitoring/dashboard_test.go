package monitoring

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDashboardIsValidAndCoversOperationalSignals(t *testing.T) {
	data, err := os.ReadFile("glider-dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatalf("invalid dashboard JSON: %v", err)
	}
	if dashboard.UID == "" || len(dashboard.Panels) < 8 {
		t.Fatalf("dashboard identity or panel coverage missing: uid=%q panels=%d", dashboard.UID, len(dashboard.Panels))
	}
	all := string(data)
	for _, metric := range []string{"glider_api_requests_total", "glider_api_request_duration_seconds_bucket", "glider_api_requests_in_flight", "glider_controller_leader", "glider_nodes", "glider_tasks", "glider_metrics_snapshot_failures_total"} {
		if !strings.Contains(all, metric) {
			t.Errorf("dashboard does not query %s", metric)
		}
	}
}
