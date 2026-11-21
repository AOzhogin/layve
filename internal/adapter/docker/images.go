// Package docker adapts the Docker Engine API to the domain model.
package docker

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	apiimage "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/domain/registry"
	"github.com/AOzhogin/layve/internal/pkg/dockerctx"
	"github.com/AOzhogin/layve/internal/pkg/sshconn"
)

// Images manages images on one Docker daemon.
type Images struct {
	cli *client.Client
}

// NewImages connects using the standard DOCKER_* environment variables.
func NewImages() (*Images, error) {
	return newImages(client.FromEnv)
}

// NewImagesFor connects to the daemon of a Docker context. The default
// context follows the DOCKER_* variables; ssh:// contexts tunnel through
// the system ssh client. Connecting is lazy: errors surface on first use.
func NewImagesFor(c dockerctx.Context) (*Images, error) {
	if c.Host == "" {
		return NewImages()
	}
	if strings.HasPrefix(c.Host, "ssh://") {
		target, err := sshconn.Parse(c.Host)
		if err != nil {
			return nil, fmt.Errorf("context %s: %w", c.Name, err)
		}
		// The tunnel decides where requests go; the host only shows up in
		// error messages, so name the remote machine there.
		return newImages(client.WithHost("http://"+target.Host), client.WithDialContext(target.Dialer("")))
	}
	opts := []client.Opt{client.WithHost(c.Host)}
	if c.TLSDir != "" {
		opts = append(opts, client.WithTLSClientConfig(
			filepath.Join(c.TLSDir, "ca.pem"), filepath.Join(c.TLSDir, "cert.pem"), filepath.Join(c.TLSDir, "key.pem")))
	}
	return newImages(opts...)
}

func newImages(opts ...client.Opt) (*Images, error) {
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Images{cli: cli}, nil
}

// Export streams images in "docker save" format; tagged targets keep their
// tags.
func (r *Images) Export(ctx context.Context, targets []image.Target) (io.ReadCloser, error) {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.String()
	}
	rc, err := r.cli.ImageSave(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return rc, nil
}

// Import loads a "docker save" stream.
func (r *Images) Import(ctx context.Context, src io.Reader) error {
	res, err := r.cli.ImageLoad(ctx, src, client.ImageLoadWithQuiet(true))
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	defer res.Close()
	if err := readLoadResponse(res); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	return nil
}

// Close releases the underlying client.
func (r *Images) Close() error { return r.cli.Close() }

// List returns all top-level local images.
func (r *Images) List(ctx context.Context) ([]image.Image, error) {
	res, err := r.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	out := make([]image.Image, 0, len(res.Items))
	for _, s := range res.Items {
		img, err := toDomain(s)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, nil
}

func toDomain(s apiimage.Summary) (image.Image, error) {
	id, err := image.NewID(s.ID)
	if err != nil {
		return image.Image{}, err
	}
	size, err := image.NewSize(s.Size)
	if err != nil {
		return image.Image{}, fmt.Errorf("image %s: %w", id.Short(), err)
	}
	refs := make([]image.Reference, 0, len(s.RepoTags))
	for _, tag := range s.RepoTags {
		ref, err := image.ParseReference(tag)
		if err != nil {
			// "<none>:<none>" placeholders mean the image is untagged.
			continue
		}
		refs = append(refs, ref)
	}
	return image.New(id, refs, size, time.Unix(s.Created, 0)), nil
}

// Find looks an image up by reference or ID.
func (r *Images) Find(ctx context.Context, name string) (image.Image, error) {
	res, err := r.cli.ImageInspect(ctx, name)
	if err != nil {
		return image.Image{}, fmt.Errorf("find %s: %w", name, err)
	}
	created, _ := time.Parse(time.RFC3339Nano, res.Created)
	return toDomain(apiimage.Summary{ID: res.ID, RepoTags: res.RepoTags, Size: res.Size, Created: created.Unix()})
}

// Platform returns the OS and architecture of the daemon.
func (r *Images) Platform(ctx context.Context) (registry.Platform, error) {
	v, err := r.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return registry.Platform{}, fmt.Errorf("server version: %w", err)
	}
	return registry.NewPlatform(v.Os, v.Arch, "")
}

// Pull downloads ref for a platform. progress gets the bytes downloaded
// and the total known so far, summed over layers.
func (r *Images) Pull(ctx context.Context, ref image.Reference, platform registry.Platform, progress func(done, total int64)) error {
	res, err := r.cli.ImagePull(ctx, ref.String(), client.ImagePullOptions{
		Platforms: []ocispec.Platform{{OS: platform.OS(), Architecture: platform.Arch(), Variant: platform.Variant()}},
	})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer res.Close()
	layers := map[string][2]int64{}
	for msg, err := range res.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if msg.ID == "" {
			continue
		}
		cur := layers[msg.ID]
		switch {
		case msg.Status == "Downloading" && msg.Progress != nil:
			cur = [2]int64{msg.Progress.Current, msg.Progress.Total}
		case msg.Status == "Download complete" || msg.Status == "Pull complete" || msg.Status == "Already exists":
			cur[0] = cur[1]
		}
		layers[msg.ID] = cur
		if progress != nil {
			var done, total int64
			for _, l := range layers {
				done += l[0]
				total += l[1]
			}
			progress(done, total)
		}
	}
	return nil
}
