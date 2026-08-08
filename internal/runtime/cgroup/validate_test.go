//go:build linux

package cgroup

import (
	"errors"
	"testing"
)

func TestValidateContainerIDAccepted(t *testing.T) {
	ok := []string{
		"0123456789abcdef",
		"deadbeefcafef00d",
		"0000000000000000",
		"ffffffffffffffff",
	}
	for _, id := range ok {
		if err := validateContainerID(id); err != nil {
			t.Errorf("validateContainerID(%q): unexpected error: %v", id, err)
		}
	}
}

func TestValidateContainerIDRejected(t *testing.T) {
	bad := []string{
		"",
		"short",
		"toolongtoolongtoolong",
		"../../../../etc/passwd",
		"../../etc",
		"/etc/passwd",
		"deadbeefcafeF00d", // uppercase
		"deadbeef cafef0d",
		"deadbeef/cafef0d",
		"deadbeef.cafef0d",
		"deadbeefcafef0dg", // 'g' not hex
	}
	for _, id := range bad {
		err := validateContainerID(id)
		if !errors.Is(err, ErrInvalidContainerID) {
			t.Errorf("validateContainerID(%q): got %v, want ErrInvalidContainerID", id, err)
		}
	}
}
