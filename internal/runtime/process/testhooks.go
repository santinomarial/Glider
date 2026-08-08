//go:build linux

package process

import "os"

// envPauseAfterCreated is a test-only synchronization hook (Phase 2 §16):
// integration tests that need to kill the launcher at a deterministic
// point right after CREATED is durably published — without sleeping and
// guessing — set this to a filesystem path. When set, the launcher writes
// that file the instant saveTransition(..., state.Created) has returned
// (i.e. only after the write is fsynced and renamed into place — the same
// durability guarantee production code relies on) and then blocks
// indefinitely, never sending "go". A test polls (bounded, not
// fixed-sleep) for the marker file's existence, then knows precisely when
// it is safe to SIGKILL the launcher and exercise the "launcher dies
// after CREATED" recovery path deterministically.
//
// This does not alter production behavior in any way when unset (the
// overwhelmingly common case — no production code path ever sets this
// env var), and it does not skip, weaken, or shortcut any real
// synchronization: it activates strictly *after* the same durable
// CREATED write every real launch performs.
const envPauseAfterCreated = "_GLIDER_TEST_PAUSE_AFTER_CREATED"

func pauseAfterCreatedForTest() {
	marker := os.Getenv(envPauseAfterCreated)
	if marker == "" {
		return
	}
	_ = os.WriteFile(marker, []byte("created\n"), 0o644)
	select {} // block forever; the test kills this process directly.
}
