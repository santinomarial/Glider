//go:build linux

package cgroup

import "fmt"

// validContainerIDChars restricts container IDs accepted as a cgroup path
// component to exactly what process.NewContainerID ever generates (16
// lowercase hex characters) — deliberately strict, not just "no slashes":
// this package treats cgroup paths as privileged kernel control interfaces
// (Phase 4 §42) and this ID can arrive here from an operator-typed CLI
// argument (`glider-runtime recover <id>`), not only from internally
// generated values. No path traversal, no empty component, no unexpected
// character set, bounded length — checked directly rather than merely
// rejecting "/" and "..".
func validateContainerID(id string) error {
	const wantLen = 16
	if len(id) != wantLen {
		return fmt.Errorf("%w: %q: must be exactly %d characters", ErrInvalidContainerID, id, wantLen)
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: %q: must be lowercase hex", ErrInvalidContainerID, id)
		}
	}
	return nil
}
