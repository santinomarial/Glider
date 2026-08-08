//go:build linux

package cgroup

import (
	"errors"
	"testing"
)

func TestParseCPUCoresValid(t *testing.T) {
	cases := map[string]float64{
		"0.5": 0.5,
		"1":   1,
		"2.5": 2.5,
		" 2 ": 2,
	}
	for in, want := range cases {
		got, err := ParseCPUCores(in)
		if err != nil {
			t.Errorf("ParseCPUCores(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseCPUCores(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseCPUCoresInvalid(t *testing.T) {
	cases := []string{"-1", "0", "nan", "NaN", "inf", "Inf", "-inf", "not-a-number", "", "99999999"}
	for _, in := range cases {
		_, err := ParseCPUCores(in)
		if !errors.Is(err, ErrInvalidResource) {
			t.Errorf("ParseCPUCores(%q): got %v, want ErrInvalidResource", in, err)
		}
	}
}

func TestParseMemoryBytesValid(t *testing.T) {
	cases := map[string]int64{
		"1":     1,
		"1024":  1024,
		"1Ki":   1024,
		"256Mi": 256 * 1024 * 1024,
		"1Gi":   1024 * 1024 * 1024,
	}
	for in, want := range cases {
		got, err := ParseMemoryBytes(in)
		if err != nil {
			t.Errorf("ParseMemoryBytes(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMemoryBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseMemoryBytesInvalid(t *testing.T) {
	cases := []string{"-100", "banana", "", "0", "1KB", "1MB", "1GB", "1K", "1M", "1G", "1mi", "1ki"}
	for _, in := range cases {
		_, err := ParseMemoryBytes(in)
		if !errors.Is(err, ErrInvalidResource) {
			t.Errorf("ParseMemoryBytes(%q): got %v, want ErrInvalidResource", in, err)
		}
	}
}

func TestParseMemoryBytesOverflow(t *testing.T) {
	_, err := ParseMemoryBytes("99999999999999999999Gi")
	if !errors.Is(err, ErrInvalidResource) {
		t.Errorf("expected overflow to be rejected as ErrInvalidResource, got %v", err)
	}
}

func TestParsePIDsMaxValid(t *testing.T) {
	got, err := ParsePIDsMax("128")
	if err != nil {
		t.Fatalf("ParsePIDsMax(128): %v", err)
	}
	if got != 128 {
		t.Errorf("ParsePIDsMax(128) = %d, want 128", got)
	}
}

func TestParsePIDsMaxInvalid(t *testing.T) {
	cases := []string{"0", "-1", "banana", ""}
	for _, in := range cases {
		_, err := ParsePIDsMax(in)
		if !errors.Is(err, ErrInvalidResource) {
			t.Errorf("ParsePIDsMax(%q): got %v, want ErrInvalidResource", in, err)
		}
	}
}

func TestCPUMaxValueUnlimited(t *testing.T) {
	got := cpuMaxValue(0)
	want := "max 100000"
	if got != want {
		t.Errorf("cpuMaxValue(0) = %q, want %q", got, want)
	}
}

func TestCPUMaxValueHalfCore(t *testing.T) {
	got := cpuMaxValue(0.5)
	want := "50000 100000"
	if got != want {
		t.Errorf("cpuMaxValue(0.5) = %q, want %q", got, want)
	}
}

func TestCPUMaxValueTwoAndHalfCores(t *testing.T) {
	got := cpuMaxValue(2.5)
	want := "250000 100000"
	if got != want {
		t.Errorf("cpuMaxValue(2.5) = %q, want %q", got, want)
	}
}

func TestCPUMaxValueTinyFractionDoesNotRoundToZero(t *testing.T) {
	got := cpuMaxValue(0.000001)
	if got == "0 100000" {
		t.Errorf("cpuMaxValue(0.000001) produced a zero quota: %q", got)
	}
}

func TestMemoryMaxValue(t *testing.T) {
	if got := memoryMaxValue(0); got != "max" {
		t.Errorf("memoryMaxValue(0) = %q, want \"max\"", got)
	}
	if got := memoryMaxValue(1024); got != "1024" {
		t.Errorf("memoryMaxValue(1024) = %q, want \"1024\"", got)
	}
}

func TestPidsMaxValue(t *testing.T) {
	if got := pidsMaxValue(0); got != "max" {
		t.Errorf("pidsMaxValue(0) = %q, want \"max\"", got)
	}
	if got := pidsMaxValue(128); got != "128" {
		t.Errorf("pidsMaxValue(128) = %q, want \"128\"", got)
	}
}
