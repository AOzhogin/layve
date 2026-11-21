package image

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// ChangeKind describes how a layer changed a path.
type ChangeKind uint8

// Change kinds.
const (
	ChangeAdded ChangeKind = iota + 1
	ChangeModified
	ChangeDeleted
)

// Symbol returns the one-character marker used in listings.
func (k ChangeKind) Symbol() string {
	switch k {
	case ChangeAdded:
		return "+"
	case ChangeModified:
		return "~"
	case ChangeDeleted:
		return "-"
	default:
		return "?"
	}
}

// String returns the kind name.
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeModified:
		return "modified"
	case ChangeDeleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// ErrInvalidChange is returned for malformed file changes.
var ErrInvalidChange = errors.New("invalid file change")

// FileChange is a single path changed by a layer.
type FileChange struct {
	path    string
	kind    ChangeKind
	size    Size
	dir     bool
	mode    fs.FileMode
	uid     int
	gid     int
	link    Link
	preview Preview
}

// NewFileChange validates a change. The path is cleaned and made relative to
// the image root, e.g. "/etc/./hosts" becomes "etc/hosts".
func NewFileChange(p string, kind ChangeKind, size Size, dir bool) (FileChange, error) {
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" {
		return FileChange{}, fmt.Errorf("%w: empty path", ErrInvalidChange)
	}
	if kind < ChangeAdded || kind > ChangeDeleted {
		return FileChange{}, fmt.Errorf("%w: unknown kind %d for %q", ErrInvalidChange, kind, clean)
	}
	return FileChange{path: clean, kind: kind, size: size, dir: dir}, nil
}

// Path returns the slash-separated path relative to the image root.
func (c FileChange) Path() string { return c.path }

// Kind returns how the path changed.
func (c FileChange) Kind() ChangeKind { return c.kind }

// Size returns the file size after the change (zero for deletions).
func (c FileChange) Size() Size { return c.size }

// Dir reports whether the path is a directory.
func (c FileChange) Dir() bool { return c.dir }
