// Package hub adapts the Docker Hub API to the registry domain model.
package hub

import (
	"context"

	"github.com/AOzhogin/layve/internal/domain/registry"
	"github.com/AOzhogin/layve/internal/pkg/dockerhub"
)

const (
	searchLimit = 50
	tagLimit    = 100
)

// Hub searches Docker Hub.
type Hub struct {
	c *dockerhub.Client
}

// New returns a Docker Hub adapter; a nil client uses the public API.
func New(c *dockerhub.Client) *Hub {
	if c == nil {
		c = dockerhub.New("", nil)
	}
	return &Hub{c: c}
}

// Search finds repositories, best matches first.
func (h *Hub) Search(ctx context.Context, query string) ([]registry.Repository, error) {
	res, err := h.c.Search(ctx, query, searchLimit)
	if err != nil {
		return nil, err
	}
	out := make([]registry.Repository, 0, len(res))
	for _, r := range res {
		repo, err := registry.NewRepository(r.Name, r.Description, r.Stars, r.Pulls, r.Official)
		if err == nil {
			out = append(out, repo)
		}
	}
	return out, nil
}

// Tags lists recent tags of repo whose name contains filter.
func (h *Hub) Tags(ctx context.Context, repo, filter string) ([]registry.Tag, error) {
	res, err := h.c.Tags(ctx, repo, filter, tagLimit)
	if err != nil {
		return nil, err
	}
	out := make([]registry.Tag, 0, len(res))
	for _, t := range res {
		var variants []registry.Variant
		seen := map[registry.Platform]bool{}
		for _, img := range t.Images {
			// Signatures and attestations show up as unknown/unknown;
			// Windows builds repeat per OS version, Docker picks the match.
			p, err := registry.NewPlatform(img.OS, img.Architecture, img.Variant)
			if err != nil || img.OS == "unknown" || seen[p] {
				continue
			}
			seen[p] = true
			variants = append(variants, registry.Variant{Platform: p, Size: img.Size})
		}
		if tag, err := registry.NewTag(t.Name, t.LastUpdated, variants); err == nil {
			out = append(out, tag)
		}
	}
	return out, nil
}
