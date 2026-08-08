package process

import "testing"

func TestParseResultRunning(t *testing.T) {
	got := parseResult(encodeRunning())
	if !got.running {
		t.Fatalf("parseResult(encodeRunning()) = %+v, want running=true", got)
	}
}

func TestParseResultFail(t *testing.T) {
	got := parseResult(encodeFail("mount /proc: permission denied"))
	if got.running {
		t.Fatalf("parseResult(encodeFail(...)) = %+v, want running=false", got)
	}
	if got.reason != "mount /proc: permission denied" {
		t.Errorf("reason = %q, want the original message", got.reason)
	}
}

func TestParseResultFailSanitizesNewlines(t *testing.T) {
	got := parseResult(encodeFail("line one\nline two\r\nline three"))
	if got.running {
		t.Fatalf("expected running=false")
	}
	if got.reason != "line one line two  line three" {
		t.Errorf("reason = %q, embedded newlines must not corrupt the single-line wire format", got.reason)
	}
}

func TestParseResultEmptyMeansUnconfirmedExit(t *testing.T) {
	got := parseResult(nil)
	if got.running {
		t.Fatalf("expected running=false for empty result (glider-init exited without confirming anything)")
	}
	if got.reason == "" {
		t.Errorf("expected a non-empty synthesized reason for empty result")
	}
}

func TestParseResultGarbageIsTreatedAsFailure(t *testing.T) {
	got := parseResult([]byte("this is not a protocol message"))
	if got.running {
		t.Fatalf("expected running=false for unrecognized content")
	}
	if got.reason == "" {
		t.Errorf("expected a non-empty synthesized reason for garbage input")
	}
}
