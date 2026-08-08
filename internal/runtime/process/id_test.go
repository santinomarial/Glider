package process

import "testing"

func TestNewContainerIDUniqueAndWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := NewContainerID()
		if err != nil {
			t.Fatalf("NewContainerID: %v", err)
		}
		if len(id) != 16 {
			t.Fatalf("expected 16 hex chars (8 bytes), got %q (len %d)", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate container id generated: %q", id)
		}
		seen[id] = true
	}
}
