// Package registry holds the domain model for images published in a
// registry such as Docker Hub.
package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrInvalidPlatform is returned for malformed platform strings.
var ErrInvalidPlatform = errors.New("invalid platform")

// Platform is an OS and CPU architecture an image is built for, e.g.
// linux/arm64/v8.
type Platform struct {
	os, arch, variant string
}

// NewPlatform validates a platform.
func NewPlatform(os, arch, variant string) (Platform, error) {
	if os == "" || arch == "" || strings.ContainsAny(os+arch+variant, "/ ") {
		return Platform{}, fmt.Errorf("%w: %q/%q/%q", ErrInvalidPlatform, os, arch, variant)
	}
	return Platform{os: os, arch: arch, variant: variant}, nil
}

// ParsePlatform reads "os/arch[/variant]".
func ParsePlatform(s string) (Platform, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	switch len(parts) {
	case 2:
		return NewPlatform(parts[0], parts[1], "")
	case 3:
		return NewPlatform(parts[0], parts[1], parts[2])
	default:
		return Platform{}, fmt.Errorf("%w: %q", ErrInvalidPlatform, s)
	}
}

// OS returns the operating system, e.g. linux.
func (p Platform) OS() string { return p.os }

// Arch returns the CPU architecture, e.g. arm64.
func (p Platform) Arch() string { return p.arch }

// Variant returns the CPU variant, e.g. v8; often empty.
func (p Platform) Variant() string { return p.variant }

// String returns "os/arch[/variant]".
func (p Platform) String() string {
	if p.variant == "" {
		return p.os + "/" + p.arch
	}
	return p.os + "/" + p.arch + "/" + p.variant
}

// Matches reports whether p can be used where want is asked for; an empty
// variant on either side matches any variant.
func (p Platform) Matches(want Platform) bool {
	return p.os == want.os && p.arch == want.arch &&
		(p.variant == "" || want.variant == "" || p.variant == want.variant)
}

// Repository is a search result.
type Repository struct {
	name        string
	description string
	stars       int
	pulls       int64
	official    bool
}

// NewRepository builds a search result; name is "nginx" or "bitnami/redis".
func NewRepository(name, description string, stars int, pulls int64, official bool) (Repository, error) {
	if strings.TrimSpace(name) == "" {
		return Repository{}, errors.New("repository name is empty")
	}
	return Repository{name: name, description: strings.TrimSpace(description), stars: stars, pulls: pulls, official: official}, nil
}

// Name returns the repository as used with docker pull.
func (r Repository) Name() string { return r.name }

// Description returns the short description.
func (r Repository) Description() string { return r.description }

// Stars returns the star count.
func (r Repository) Stars() int { return r.stars }

// Pulls returns the pull count.
func (r Repository) Pulls() int64 { return r.pulls }

// Official reports whether it is a Docker Official Image.
func (r Repository) Official() bool { return r.official }

// Variant is one platform build of a tag.
type Variant struct {
	Platform Platform
	Size     int64
}

// Tag is a published version of a repository with its platform builds.
type Tag struct {
	name     string
	updated  time.Time
	variants []Variant
}

// NewTag builds a tag; variants are sorted by platform.
func NewTag(name string, updated time.Time, variants []Variant) (Tag, error) {
	if strings.TrimSpace(name) == "" {
		return Tag{}, errors.New("tag name is empty")
	}
	v := slices.Clone(variants)
	slices.SortFunc(v, func(a, b Variant) int { return strings.Compare(a.Platform.String(), b.Platform.String()) })
	return Tag{name: name, updated: updated, variants: v}, nil
}

// Name returns the tag, e.g. 1.27-alpine.
func (t Tag) Name() string { return t.name }

// Updated returns when the tag was last pushed.
func (t Tag) Updated() time.Time { return t.updated }

// Variants returns the platform builds.
func (t Tag) Variants() []Variant { return slices.Clone(t.variants) }

// Find returns the build for a platform.
func (t Tag) Find(want Platform) (Variant, bool) {
	for _, v := range t.variants {
		if v.Platform.Matches(want) {
			return v, true
		}
	}
	return Variant{}, false
}
