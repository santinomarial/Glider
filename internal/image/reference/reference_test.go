package reference

import (
	"errors"
	"testing"
)

func TestParseNormalizesSupportedReferences(t *testing.T) {
	cases := map[string]string{
		"alpine":                       "registry-1.docker.io/library/alpine:latest",
		"alpine:3.21":                  "registry-1.docker.io/library/alpine:3.21",
		"example/api":                  "registry-1.docker.io/example/api:latest",
		"ghcr.io/example/api:v17":      "ghcr.io/example/api:v17",
		"localhost:5000/team/app:test": "localhost:5000/team/app:test",
		"repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "registry-1.docker.io/library/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for input, want := range cases {
		got, err := Parse(input)
		if err != nil {
			t.Errorf("Parse(%q): %v", input, err)
			continue
		}
		if got.String() != want {
			t.Errorf("Parse(%q) = %q, want %q", input, got.String(), want)
		}
	}
}

func TestParseRejectsAmbiguousOrUnsafeReferences(t *testing.T) {
	for _, input := range []string{"", "https://example.com/a", "UPPER/repo", "repo:", "repo@", "repo@md5:abcd", "host/repo/../bad", "/repo"} {
		if _, err := Parse(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalid", input, err)
		}
	}
}
