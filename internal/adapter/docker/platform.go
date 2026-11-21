package docker

import (
	"context"
	"fmt"
	"io"

	apiimage "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/AOzhogin/layve/internal/pkg/imagearchive"
)

// localPlatform picks the platform to read an image as.
//
// The classic image store keeps one platform per image and needs none: it
// returns nil. The containerd image store (the default since Docker 29)
// keeps the whole multi-platform index of which only some platforms may
// have been pulled; without an explicit platform the daemon uses its own,
// and "save" or "history" fail with "content digest … not found" when only
// another one is present — e.g. after pulling linux/arm64 on an amd64 host.
// The daemon's own platform wins when it is available, otherwise the first
// pulled image manifest.
func (r *Images) localPlatform(ctx context.Context, name string) (*ocispec.Platform, error) {
	res, err := r.cli.ImageInspect(ctx, name, client.ImageInspectWithManifests(true))
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	var available []ocispec.Platform
	for _, m := range res.Manifests {
		if m.Kind == apiimage.ManifestKindImage && m.Available && m.ImageData != nil {
			available = append(available, m.ImageData.Platform)
		}
	}
	if len(available) == 0 {
		return nil, nil
	}
	if native, err := r.Platform(ctx); err == nil {
		for _, p := range available {
			if p.OS == native.OS() && p.Architecture == native.Arch() {
				return &p, nil
			}
		}
	}
	return &available[0], nil
}

// save streams images in "docker save" format as the platforms that are
// present locally, so partly pulled multi-platform images export too. The
// daemon takes one platform list for all names and needs every listed
// platform in every image, so images of different platforms are saved one
// by one and merged into a single archive.
func (r *Images) save(ctx context.Context, names []string) (io.ReadCloser, error) {
	type item struct {
		name     string
		platform *ocispec.Platform
	}
	var items []item
	keys := map[string]bool{}
	for _, name := range names {
		p, err := r.localPlatform(ctx, name)
		if err != nil {
			return nil, err
		}
		items = append(items, item{name, p})
		keys[platformKey(p)] = true
	}
	saveAs := func(names []string, p *ocispec.Platform) (io.ReadCloser, error) {
		var opts []client.ImageSaveOption
		if p != nil {
			opts = append(opts, client.ImageSaveWithPlatforms(*p))
		}
		return r.cli.ImageSave(ctx, names, opts...)
	}
	if len(keys) <= 1 {
		return saveAs(names, items[0].platform)
	}

	openers := make([]func() (io.ReadCloser, error), len(items))
	for i, it := range items {
		openers[i] = func() (io.ReadCloser, error) { return saveAs([]string{it.name}, it.platform) }
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(imagearchive.Merge(pw, openers)) }()
	return pr, nil
}

func platformKey(p *ocispec.Platform) string {
	if p == nil {
		return ""
	}
	return p.OS + "/" + p.Architecture + "/" + p.Variant
}
