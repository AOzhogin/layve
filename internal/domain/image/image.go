// Package image holds the domain model for local container images.
package image

import (
	"slices"
	"time"
)

// Image is a locally stored container image.
type Image struct {
	id         ID
	references []Reference
	size       Size
	created    time.Time
}

// New builds an Image. A nil or empty references slice marks it as dangling.
func New(id ID, references []Reference, size Size, created time.Time) Image {
	return Image{
		id:         id,
		references: slices.Clone(references),
		size:       size,
		created:    created,
	}
}

// ID returns the image identifier.
func (i Image) ID() ID { return i.id }

// References returns a copy of the tags pointing at this image.
func (i Image) References() []Reference { return slices.Clone(i.references) }

// Size returns the image size.
func (i Image) Size() Size { return i.size }

// Created returns when the image was built.
func (i Image) Created() time.Time { return i.created }

// Dangling reports whether no tag points at this image.
func (i Image) Dangling() bool { return len(i.references) == 0 }
