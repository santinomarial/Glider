// Package reference parses the deliberately small OCI/Docker image-reference
// subset Glider supports in Phase 5. It owns naming only; registry I/O belongs
// to internal/image/registry.
package reference

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

var ErrInvalid = errors.New("invalid image reference")

var componentRE = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// Reference is a normalized registry/repository selector. Exactly one of Tag
// and Digest is populated.
type Reference struct {
	Original   string
	Registry   string
	Repository string
	Tag        string
	Digest     digest.Digest
}

// Parse accepts name[:tag] and name@digest. Familiar short names use Docker
// Hub conventions: alpine -> registry-1.docker.io/library/alpine:latest.
func Parse(input string) (Reference, error) {
	var ref Reference
	input = strings.TrimSpace(input)
	if input == "" || strings.Contains(input, "://") {
		return ref, fmt.Errorf("%w: %q", ErrInvalid, input)
	}
	ref.Original = input

	name := input
	if at := strings.LastIndexByte(input, '@'); at >= 0 {
		if strings.Count(input, "@") != 1 || at == 0 || at == len(input)-1 {
			return ref, fmt.Errorf("%w: malformed digest reference %q", ErrInvalid, input)
		}
		name = input[:at]
		d, err := digest.Parse(input[at+1:])
		if err != nil || d.Algorithm() != digest.SHA256 || d.Validate() != nil {
			return ref, fmt.Errorf("%w: unsupported or malformed digest in %q", ErrInvalid, input)
		}
		ref.Digest = d
	} else {
		lastSlash := strings.LastIndexByte(name, '/')
		lastColon := strings.LastIndexByte(name, ':')
		if lastColon > lastSlash {
			ref.Tag = name[lastColon+1:]
			name = name[:lastColon]
			if !tagRE.MatchString(ref.Tag) {
				return Reference{}, fmt.Errorf("%w: invalid tag in %q", ErrInvalid, input)
			}
		} else {
			ref.Tag = "latest"
		}
	}

	parts := strings.Split(name, "/")
	if len(parts) == 0 || parts[0] == "" {
		return Reference{}, fmt.Errorf("%w: missing repository in %q", ErrInvalid, input)
	}
	if len(parts) > 1 && isRegistry(parts[0]) {
		ref.Registry = parts[0]
		parts = parts[1:]
	} else {
		ref.Registry = "registry-1.docker.io"
		if len(parts) == 1 {
			parts = append([]string{"library"}, parts...)
		}
	}
	if len(parts) == 0 {
		return Reference{}, fmt.Errorf("%w: missing repository in %q", ErrInvalid, input)
	}
	for _, part := range parts {
		if !componentRE.MatchString(part) {
			return Reference{}, fmt.Errorf("%w: invalid repository component %q", ErrInvalid, part)
		}
	}
	if strings.ContainsAny(ref.Registry, "@/ \\") || ref.Registry == "" {
		return Reference{}, fmt.Errorf("%w: invalid registry in %q", ErrInvalid, input)
	}
	ref.Repository = strings.Join(parts, "/")
	return ref, nil
}

func isRegistry(first string) bool {
	return first == "localhost" || strings.Contains(first, ".") || strings.Contains(first, ":")
}

// Selector returns the value used in a registry manifests URL.
func (r Reference) Selector() string {
	if r.Digest != "" {
		return r.Digest.String()
	}
	return r.Tag
}

func (r Reference) String() string {
	base := r.Registry + "/" + r.Repository
	if r.Digest != "" {
		return base + "@" + r.Digest.String()
	}
	return base + ":" + r.Tag
}
