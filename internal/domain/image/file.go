package image

import "io/fs"

// Link describes where a symbolic or hard link points.
type Link struct {
	target   string
	resolved string
	hard     bool
	broken   bool
}

// NewSymlink describes a symbolic link. target is as written in the image,
// resolved is the final path after following every link in the image.
func NewSymlink(target, resolved string, broken bool) Link {
	return Link{target: target, resolved: resolved, broken: broken}
}

// NewHardlink describes a hard link to another path in the image.
func NewHardlink(target string) Link { return Link{target: target, hard: true} }

// IsZero reports whether the file is not a link.
func (l Link) IsZero() bool { return l.target == "" }

// Target returns the link target as written.
func (l Link) Target() string { return l.target }

// Resolved returns where a symlink finally leads (image-relative path).
func (l Link) Resolved() string { return l.resolved }

// Hard reports whether this is a hard link.
func (l Link) Hard() bool { return l.hard }

// Broken reports whether a symlink leads nowhere or loops.
func (l Link) Broken() bool { return l.broken }

// PreviewState tells whether file content can be shown.
type PreviewState uint8

// Preview states.
const (
	PreviewNone PreviewState = iota
	PreviewText
	PreviewBinary
	PreviewTooLarge
	PreviewSkipped
)

// Preview is the text of a file, or the reason it is not available.
type Preview struct {
	state PreviewState
	text  string
}

// NewTextPreview wraps file text.
func NewTextPreview(text string) Preview { return Preview{state: PreviewText, text: text} }

// NewUnavailablePreview records why there is no text.
func NewUnavailablePreview(state PreviewState) Preview {
	if state == PreviewText {
		state = PreviewNone
	}
	return Preview{state: state}
}

// State returns the preview state.
func (p Preview) State() PreviewState { return p.state }

// Text returns the file text when State is PreviewText.
func (p Preview) Text() string { return p.text }

// WithMode returns a copy with permissions and ownership.
func (c FileChange) WithMode(mode fs.FileMode, uid, gid int) FileChange {
	c.mode, c.uid, c.gid = mode, uid, gid
	return c
}

// WithLink returns a copy that is a link.
func (c FileChange) WithLink(l Link) FileChange {
	c.link = l
	return c
}

// WithPreview returns a copy carrying content preview.
func (c FileChange) WithPreview(p Preview) FileChange {
	c.preview = p
	return c
}

// Mode returns the file mode, including type bits.
func (c FileChange) Mode() fs.FileMode { return c.mode }

// Owner returns the numeric user and group IDs.
func (c FileChange) Owner() (uid, gid int) { return c.uid, c.gid }

// Link returns link details; IsZero when the file is not a link.
func (c FileChange) Link() Link { return c.link }

// Preview returns the content preview.
func (c FileChange) Preview() Preview { return c.preview }
