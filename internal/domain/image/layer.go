package image

import (
	"strings"
	"time"
)

// Layer is one step of an image's build history. Steps such as ENV or CMD
// change only metadata and have no filesystem layer; Empty reports those.
type Layer struct {
	createdBy string
	size      Size
	created   time.Time
	empty     bool
}

// NewLayer builds a history entry.
func NewLayer(createdBy string, size Size, created time.Time, empty bool) Layer {
	return Layer{createdBy: normalizeCommand(createdBy), size: size, created: created, empty: empty}
}

// CreatedBy returns the Dockerfile instruction that produced the layer.
func (l Layer) CreatedBy() string { return l.createdBy }

// Size returns the layer size.
func (l Layer) Size() Size { return l.size }

// Created returns when the layer was built.
func (l Layer) Created() time.Time { return l.created }

// Empty reports whether the step changed metadata only.
func (l Layer) Empty() bool { return l.empty }

// normalizeCommand strips the classic-builder shell wrapper so that
// "/bin/sh -c #(nop) CMD [...]" reads as "CMD [...]" and a plain
// "/bin/sh -c apt-get ..." reads as "RUN apt-get ...".
func normalizeCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	const shell = "/bin/sh -c "
	if rest, ok := strings.CutPrefix(cmd, shell+"#(nop) "); ok {
		return strings.TrimSpace(rest)
	}
	if rest, ok := strings.CutPrefix(cmd, shell); ok {
		return "RUN " + rest
	}
	return strings.TrimSuffix(cmd, " # buildkit")
}
