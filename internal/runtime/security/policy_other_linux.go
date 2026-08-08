//go:build linux && !amd64 && !arm64

package security

func architecturePolicy() (uint32, []uintptr, error) { return 0, nil, ErrUnsupportedArchitecture }
