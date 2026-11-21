package image

import (
	"errors"

	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// ErrNegativeSize is returned when a size is below zero.
var ErrNegativeSize = errors.New("image size is negative")

// Size is an image size in bytes.
type Size struct {
	bytes int64
}

// NewSize validates and wraps a byte count.
func NewSize(bytes int64) (Size, error) {
	if bytes < 0 {
		return Size{}, ErrNegativeSize
	}
	return Size{bytes: bytes}, nil
}

// Bytes returns the raw byte count.
func (s Size) Bytes() int64 { return s.bytes }

// String returns a human-readable size such as "188MB".
func (s Size) String() string { return humanize.Bytes(s.bytes) }
