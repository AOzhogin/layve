package image

import (
	"errors"
	"strings"
)

// ErrEmptyID is returned when an image ID is blank.
var ErrEmptyID = errors.New("image id is empty")

const shortIDLen = 12

// ID is a content-addressable image identifier such as "sha256:abc...".
type ID struct {
	value string
}

// NewID validates and wraps a raw image ID.
func NewID(raw string) (ID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ID{}, ErrEmptyID
	}
	return ID{value: raw}, nil
}

// String returns the full ID including the algorithm prefix.
func (id ID) String() string { return id.value }

// Short returns the first 12 hex characters without the algorithm prefix,
// the same form the docker CLI prints.
func (id ID) Short() string {
	hex := id.value
	if _, after, ok := strings.Cut(hex, ":"); ok {
		hex = after
	}
	if len(hex) > shortIDLen {
		return hex[:shortIDLen]
	}
	return hex
}
