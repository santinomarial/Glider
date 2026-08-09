//go:build linux

package agent

import "testing"

func TestDiskPressuredHonorsAbsoluteAndPercentageThresholds(t *testing.T) {
	tests := []struct {
		name                       string
		total, available, minBytes uint64
		minPercent                 float64
		want                       bool
	}{
		{"healthy", 1000, 200, 100, 10, false},
		{"absolute", 1000, 99, 100, 0, true},
		{"percentage", 1000, 99, 0, 10, true},
		{"exact thresholds", 1000, 100, 100, 10, false},
		{"zero sized filesystem", 0, 0, 0, 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diskPressured(tt.total, tt.available, tt.minBytes, tt.minPercent); got != tt.want {
				t.Fatalf("diskPressured() = %v, want %v", got, tt.want)
			}
		})
	}
}
