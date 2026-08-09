package observability

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestLoggerEmitsReservedFieldSafeJSON(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(&output, "gliderd")
	logger.now = func() time.Time { return time.Unix(1, 2).UTC() }
	logger.Info("node ready", map[string]any{"node_id": "node-a", "level": "forged"})
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["level"] != "info" || record["component"] != "gliderd" || record["node_id"] != "node-a" || record["message"] != "node ready" {
		t.Fatalf("record=%v", record)
	}
}
