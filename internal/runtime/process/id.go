// Package process implements the parent/child launch, synchronization, and
// lifecycle-state machinery described in docs/design/runtime.md.
package process

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewContainerID generates a random, node-local container identifier.
//
// Phase 1 has no control-plane TaskID/Generation to derive an identity from
// (docs/architecture/overview.md §4) — glider-runtime is invoked standalone.
// The ID only needs to be unique enough to keep concurrent local invocations
// from colliding on the same state file (runtime.md §6 exit gate (f)); 8
// random bytes is ample for that and deliberately not over-engineered into
// a sortable/structured ID scheme that Phase 1 has no use for.
func NewContainerID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate container id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
