package image

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// EditKind names an image edit operation.
type EditKind uint8

// Edit kinds.
const (
	EditDropLayer EditKind = iota + 1
	EditMergeLayers
	EditRemoveFromLayer
	EditPurgePath
	EditPutFile
)

// ErrInvalidEdit is returned for malformed edits.
var ErrInvalidEdit = errors.New("invalid edit")

// Edit is one change to an image. Steps are history indexes of the image
// as it looks after the previous edits of the plan.
type Edit struct {
	kind     EditKind
	step     int
	toStep   int
	path     string
	content  []byte
	hostPath string
	mode     fs.FileMode
	uid, gid int
	size     int64
}

// DropLayer removes the layer of a step.
func DropLayer(step int) Edit { return Edit{kind: EditDropLayer, step: step} }

// MergeLayers folds the layers of steps from..to into one.
func MergeLayers(from, to int) Edit {
	return Edit{kind: EditMergeLayers, step: min(from, to), toStep: max(from, to)}
}

// RemoveFromLayer undoes a step's change to a path; a lower version, if
// any, shows through again.
func RemoveFromLayer(step int, p string) (Edit, error) {
	clean, err := editPath(p)
	return Edit{kind: EditRemoveFromLayer, step: step, path: clean}, err
}

// PurgePath removes every version of a path from the image.
func PurgePath(p string) (Edit, error) {
	clean, err := editPath(p)
	return Edit{kind: EditPurgePath, path: clean}, err
}

// PutFile adds or replaces a file in a step's layer with the given content.
func PutFile(step int, p string, content []byte, mode fs.FileMode, uid, gid int) (Edit, error) {
	clean, err := editPath(p)
	return Edit{
		kind: EditPutFile, step: step, path: clean, content: slices.Clone(content),
		mode: mode.Perm(), uid: uid, gid: gid, size: int64(len(content)),
	}, err
}

// PutHostFile adds a file whose content is read from the host when the
// image is written.
func PutHostFile(step int, p, hostPath string, mode fs.FileMode, size int64) (Edit, error) {
	clean, err := editPath(p)
	if err == nil && hostPath == "" {
		err = fmt.Errorf("%w: empty host path", ErrInvalidEdit)
	}
	return Edit{kind: EditPutFile, step: step, path: clean, hostPath: hostPath, mode: mode.Perm(), size: size}, err
}

func editPath(p string) (string, error) {
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" {
		return "", fmt.Errorf("%w: path is the root directory", ErrInvalidEdit)
	}
	return clean, nil
}

// Kind returns the operation.
func (e Edit) Kind() EditKind { return e.kind }

// Steps returns the target step, and the last step for merges.
func (e Edit) Steps() (from, to int) { return e.step, e.toStep }

// Path returns the image path for file edits.
func (e Edit) Path() string { return e.path }

// File returns the content, host path, mode, owner and size of PutFile edits.
func (e Edit) File() (content []byte, hostPath string, mode fs.FileMode, uid, gid int, size int64) {
	return slices.Clone(e.content), e.hostPath, e.mode, e.uid, e.gid, e.size
}

// String describes the edit for humans, with 1-based step numbers.
func (e Edit) String() string {
	switch e.kind {
	case EditDropLayer:
		return fmt.Sprintf("drop layer %d", e.step+1)
	case EditMergeLayers:
		return fmt.Sprintf("merge layers %d–%d", e.step+1, e.toStep+1)
	case EditRemoveFromLayer:
		return fmt.Sprintf("remove /%s from layer %d", e.path, e.step+1)
	case EditPurgePath:
		return fmt.Sprintf("purge /%s from image", e.path)
	case EditPutFile:
		if e.hostPath != "" {
			return fmt.Sprintf("add %s as /%s in layer %d", e.hostPath, e.path, e.step+1)
		}
		return fmt.Sprintf("write /%s in layer %d", e.path, e.step+1)
	default:
		return "unknown edit"
	}
}

// EditPlan is an ordered, immutable list of edits.
type EditPlan struct {
	edits []Edit
}

// With returns a plan with e appended.
func (p EditPlan) With(e Edit) EditPlan {
	return EditPlan{edits: append(slices.Clone(p.edits), e)}
}

// WithAll returns a plan with all edits appended.
func (p EditPlan) WithAll(edits ...Edit) EditPlan {
	return EditPlan{edits: append(slices.Clone(p.edits), edits...)}
}

// Undo returns the plan without its last edit.
func (p EditPlan) Undo() EditPlan {
	if len(p.edits) == 0 {
		return p
	}
	return EditPlan{edits: slices.Clone(p.edits[:len(p.edits)-1])}
}

// Len returns the number of edits.
func (p EditPlan) Len() int { return len(p.edits) }

// Edits returns a copy of the edits in order.
func (p EditPlan) Edits() []Edit { return slices.Clone(p.edits) }

// WriteOptions tunes how an edited image is saved.
type WriteOptions struct {
	// MarkHistory notes "edited with layve" on the history steps of
	// changed layers, so anyone running "docker history" can tell.
	MarkHistory bool
}

// HistoryMark is the comment WriteOptions.MarkHistory adds.
const HistoryMark = "edited with layve"
