package process

import "strings"

// The launcher<->glider-init result channel (fdResultWrite/fdResultRead,
// config.go) is a small, explicit textual protocol — successor to Phase
// 1's implicit "unified error channel" (runtime.md §7), which relied on a
// successful execve automatically closing a CLOEXEC pipe to mean "success".
// That trick doesn't apply once glider-init stops exec'ing itself into the
// workload (docs/adr/0006): a bare EOF with no data is now ambiguous
// between "explicit success" and "glider-init crashed before saying
// anything", so success requires an explicit marker.
//
// Exactly one message is ever written to this channel, then it is closed:
//   - "FAIL <reason>\n"  — setup or workload-launch failed; no workload
//     ever ran (container-lifecycle.md FAILED).
//   - "RUNNING\n"        — the workload's execve succeeded and is now
//     running (container-lifecycle.md RUNNING). No payload is needed:
//     unlike a namespace-internal PID, the workload's *host-visible* PID
//     isn't observable from inside its own PID namespace by any syscall,
//     so the launcher resolves it itself, best-effort, via the host's own
//     /proc (see resolveChildIdentity in identity_linux.go).
//
// Anything else read from the channel — empty content, a partial line, or
// unparseable content — means glider-init exited without ever confirming
// an outcome (e.g. it crashed or was killed mid-setup) and is treated as a
// failure by the reader.
const (
	resultPrefixFail  = "FAIL "
	resultLineRunning = "RUNNING"
)

func encodeFail(reason string) []byte {
	return []byte(resultPrefixFail + sanitizeResultLine(reason) + "\n")
}

func encodeRunning() []byte {
	return []byte(resultLineRunning + "\n")
}

// sanitizeResultLine strips characters that would corrupt the single-line
// wire format; failure reasons are free-form error text that could
// otherwise contain a newline.
func sanitizeResultLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// resultOutcome is the parsed, terminal meaning of whatever bytes were read
// from the result channel before it closed.
type resultOutcome struct {
	running bool
	reason  string // set when !running: either the FAIL reason, or a
	// synthesized explanation for unparseable/empty content.
}

// parseResult interprets the full contents read from the result channel
// (already read to EOF by the caller — the channel is small and
// short-lived, a single small Read/ReadAll is simpler and sufficient here
// rather than a streaming line reader).
func parseResult(data []byte) resultOutcome {
	line := strings.TrimRight(string(data), "\n")
	switch {
	case line == resultLineRunning:
		return resultOutcome{running: true}
	case strings.HasPrefix(line, resultPrefixFail):
		return resultOutcome{reason: strings.TrimPrefix(line, resultPrefixFail)}
	case line == "":
		return resultOutcome{reason: "container init exited before confirming workload start"}
	default:
		return resultOutcome{reason: "container init sent an unrecognized result: " + line}
	}
}
