// Package ui implements the terminal interface.
package ui

import (
	"context"
	"io"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/domain/registry"
)

// ImageService is what the UI needs from the Docker daemon.
type ImageService interface {
	List(ctx context.Context) ([]image.Image, error)
	Remove(ctx context.Context, target image.Target, force bool) error
	Tag(ctx context.Context, source image.Target, ref image.Reference) error
	History(ctx context.Context, id image.ID) ([]image.Layer, error)
	// Inspect reads the image's files; progress gets bytes read so far.
	Inspect(ctx context.Context, id image.ID, progress func(read int64)) (Inspection, error)
	// Write saves the image with plan applied as a new image tagged ref.
	Write(ctx context.Context, id image.ID, plan image.EditPlan, ref image.Reference, opts image.WriteOptions, progress func(stage string)) error
	// Export streams images in "docker save" format; Import loads such a
	// stream. Together they copy images between daemons.
	Export(ctx context.Context, targets []image.Target) (io.ReadCloser, error)
	Import(ctx context.Context, src io.Reader) error
	// Platform is the daemon's OS and architecture; Pull downloads an
	// image for a platform, reporting bytes done and total.
	Platform(ctx context.Context) (registry.Platform, error)
	Pull(ctx context.Context, ref image.Reference, platform registry.Platform, progress func(done, total int64)) error
	Close() error
}

// Endpoint is a Docker context the user can switch to or copy images to.
type Endpoint struct {
	Name        string
	Description string
	Address     string
}

// Connector lists Docker contexts and connects to them.
type Connector interface {
	Endpoints() ([]Endpoint, error)
	Connect(name string) (ImageService, error)
}

// Inspection previews an image with edits applied.
type Inspection interface {
	Details(plan image.EditPlan) ([]image.LayerDetail, error)
}
