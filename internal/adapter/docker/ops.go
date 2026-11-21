package docker

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/moby/moby/client"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/imagearchive"
)

// Remove deletes a tag, or the image itself when the target is an ID or the
// last tag. Force also removes images used by stopped containers.
func (r *Images) Remove(ctx context.Context, target image.Target, force bool) error {
	_, err := r.cli.ImageRemove(ctx, target.String(), client.ImageRemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		return fmt.Errorf("remove %s: %w", target.Display(), err)
	}
	return nil
}

// Tag points ref at the source image.
func (r *Images) Tag(ctx context.Context, source image.Target, ref image.Reference) error {
	_, err := r.cli.ImageTag(ctx, client.ImageTagOptions{Source: source.String(), Target: ref.String()})
	if err != nil {
		return fmt.Errorf("tag %s as %s: %w", source.Display(), ref, err)
	}
	return nil
}

// History returns the build steps of an image, oldest first. It is fast but
// cannot tell metadata-only steps apart from tiny layers; Inspect can.
func (r *Images) History(ctx context.Context, id image.ID) ([]image.Layer, error) {
	res, err := r.cli.ImageHistory(ctx, id.String())
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", id.Short(), err)
	}
	layers := make([]image.Layer, 0, len(res.Items))
	for _, h := range slices.Backward(res.Items) {
		size, err := image.NewSize(h.Size)
		if err != nil {
			return nil, fmt.Errorf("history %s: %w", id.Short(), err)
		}
		layers = append(layers, image.NewLayer(h.CreatedBy, size, time.Unix(h.Created, 0), h.Size == 0))
	}
	return layers, nil
}

func toChanges(diff []imagearchive.Change) ([]image.FileChange, error) {
	out := make([]image.FileChange, 0, len(diff))
	for _, c := range diff {
		size, err := image.NewSize(c.Size)
		if err != nil {
			return nil, err
		}
		fc, err := image.NewFileChange(c.Path, changeKind(c.Kind), size, c.Dir)
		if err != nil {
			return nil, err
		}
		fc = fc.WithMode(c.Mode, c.UID, c.GID).WithPreview(preview(c))
		switch {
		case c.Symlink != "":
			fc = fc.WithLink(image.NewSymlink(c.Symlink, c.Resolved, c.Broken))
		case c.Hardlink != "":
			fc = fc.WithLink(image.NewHardlink(c.Hardlink))
		}
		out = append(out, fc)
	}
	return out, nil
}

func preview(c imagearchive.Change) image.Preview {
	switch c.Preview {
	case imagearchive.PreviewText:
		return image.NewTextPreview(string(c.Content))
	case imagearchive.PreviewBinary:
		return image.NewUnavailablePreview(image.PreviewBinary)
	case imagearchive.PreviewTooLarge:
		return image.NewUnavailablePreview(image.PreviewTooLarge)
	case imagearchive.PreviewSkipped:
		return image.NewUnavailablePreview(image.PreviewSkipped)
	default:
		return image.NewUnavailablePreview(image.PreviewNone)
	}
}

func changeKind(k imagearchive.ChangeKind) image.ChangeKind {
	switch k {
	case imagearchive.Added:
		return image.ChangeAdded
	case imagearchive.Modified:
		return image.ChangeModified
	case imagearchive.Deleted:
		return image.ChangeDeleted
	default:
		return 0
	}
}

type countingReader struct {
	r      io.Reader
	read   int64
	report func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	c.report(c.read)
	return n, err
}
