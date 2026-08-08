package process

import (
	"strconv"
	"testing"
)

// statLine builds a synthetic /proc/<pid>/stat line. afterComm supplies the
// fields from "state" (field 3) through at least "starttime" (field 22);
// extra trailing fields are appended to mimic the real file's length.
func statLine(pid int, comm string, afterComm ...string) string {
	line := strconv.Itoa(pid) + " (" + comm + ")"
	for _, f := range afterComm {
		line += " " + f
	}
	return line
}

// normalAfterComm returns 19 filler fields (state through itrealvalue,
// fields 3-21) followed by starttime (field 22) and a couple of trailing
// fields (vsize, rss) present in the real file.
func normalAfterComm(state string, starttime uint64) []string {
	filler := []string{state, "100", "123", "123", "34816", "123", "4194304",
		"100", "0", "5", "0", "50", "10", "5", "2", "20", "0", "1", "0"}
	return append(filler, strconv.FormatUint(starttime, 10), "10000000", "500")
}

func TestParseStatNormal(t *testing.T) {
	line := statLine(1234, "bash", normalAfterComm("S", 987654321)...)
	info, err := parseStat([]byte(line))
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if info.StartTime != 987654321 {
		t.Errorf("StartTime = %d, want 987654321", info.StartTime)
	}
	if info.State != 'S' {
		t.Errorf("State = %q, want 'S'", info.State)
	}
}

func TestParseStatCommWithSpaces(t *testing.T) {
	line := statLine(5678, "my process name", normalAfterComm("R", 42)...)
	info, err := parseStat([]byte(line))
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if info.StartTime != 42 {
		t.Errorf("StartTime = %d, want 42", info.StartTime)
	}
}

func TestParseStatCommWithParens(t *testing.T) {
	// A process can rename itself (prctl PR_SET_NAME / argv[0]) to contain
	// literal parentheses, including one engineered to look like the comm
	// field closed early. Parsing must anchor on the LAST ')' in the line.
	line := statLine(9012, "evil) 999 (fake", normalAfterComm("Z", 555)...)
	info, err := parseStat([]byte(line))
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if info.StartTime != 555 {
		t.Errorf("StartTime = %d, want 555 (comm-with-parens confused the parser)", info.StartTime)
	}
	if info.State != 'Z' {
		t.Errorf("State = %q, want 'Z'", info.State)
	}
}

func TestParseStatZombieState(t *testing.T) {
	line := statLine(1, "init", normalAfterComm("Z", 1)...)
	info, err := parseStat([]byte(line))
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if info.State != 'Z' {
		t.Errorf("State = %q, want 'Z'", info.State)
	}
}

func TestParseStatTruncatedNoClosingParen(t *testing.T) {
	_, err := parseStat([]byte("1234 (bash S 100"))
	if err == nil {
		t.Fatal("expected error for missing closing paren, got nil")
	}
}

func TestParseStatTruncatedTooFewFields(t *testing.T) {
	// Only a handful of fields after comm — nowhere near starttime (field 22).
	_, err := parseStat([]byte("1234 (bash) S 100 123 123"))
	if err == nil {
		t.Fatal("expected error for too few fields, got nil")
	}
}

func TestParseStatEmpty(t *testing.T) {
	_, err := parseStat([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

func TestParseStatNonNumericStartTime(t *testing.T) {
	after := normalAfterComm("S", 0)
	after[19] = "not-a-number" // starttime index within afterComm slice
	line := statLine(1, "bash", after...)
	_, err := parseStat([]byte(line))
	if err == nil {
		t.Fatal("expected error for non-numeric starttime, got nil")
	}
}
