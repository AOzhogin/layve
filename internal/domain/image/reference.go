package image

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultTag is the tag implied when a reference has none.
const DefaultTag = "latest"

// ErrInvalidReference is returned for references that cannot be parsed.
var ErrInvalidReference = errors.New("invalid image reference")

// Reference is a repository plus tag, e.g. "docker.io/library/nginx:1.27".
type Reference struct {
	repository string
	tag        string
}

// ParseReference parses "repo[:tag]". A colon belongs to the tag only when it
// appears after the last slash, so "localhost:5000/app" keeps its port.
// The placeholder "<none>:<none>" is rejected.
func ParseReference(raw string) (Reference, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "<none>") || strings.ContainsAny(raw, " \t@") {
		return Reference{}, fmt.Errorf("%w: %q", ErrInvalidReference, raw)
	}
	repo, tag := raw, DefaultTag
	if i := strings.LastIndex(raw, ":"); i > strings.LastIndex(raw, "/") {
		repo, tag = raw[:i], raw[i+1:]
	}
	if repo == "" || tag == "" {
		return Reference{}, fmt.Errorf("%w: %q", ErrInvalidReference, raw)
	}
	return Reference{repository: repo, tag: tag}, nil
}

// Repository returns the repository part, without the tag.
func (r Reference) Repository() string { return r.repository }

// Tag returns the tag part.
func (r Reference) Tag() string { return r.tag }

// String returns "repository:tag".
func (r Reference) String() string { return r.repository + ":" + r.tag }
