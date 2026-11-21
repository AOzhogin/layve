package imagearchive

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// NewFile is a file added to or replacing one in a layer.
type NewFile struct {
	Path string
	Mode fs.FileMode
	UID  int
	GID  int
	// Content holds the data; when nil, HostPath is read at build time.
	Content  []byte
	HostPath string
	Size     int64
}

// Part is one ingredient of a virtual layer: an original layer with some
// paths filtered out, or a set of added files.
type Part struct {
	// Source is the original layer index, or -1 for an added-files part.
	Source int
	// Remove drops these paths, and everything under them, from Source.
	Remove []string
	Files  []NewFile
}

func (p Part) removes(entryPath string) bool {
	for _, r := range p.Remove {
		if entryPath == r || strings.HasPrefix(entryPath, r+"/") {
			return true
		}
	}
	return false
}

// VLayer is a layer of the edited image, built from parts applied in order.
type VLayer struct {
	Parts  []Part
	Edited bool
}

// untouched reports whether the layer is an original layer as is.
func (v VLayer) untouched() bool {
	return len(v.Parts) == 1 && v.Parts[0].Source >= 0 && len(v.Parts[0].Remove) == 0 && !v.Edited
}

func (v VLayer) sources() []int {
	var out []int
	for _, p := range v.Parts {
		if p.Source >= 0 {
			out = append(out, p.Source)
		}
	}
	return out
}

// State is an image being edited: the original archive plus virtual layers.
type State struct {
	orig    *Archive
	vlayers []VLayer
}

// NewState starts editing an archive; initially every layer is untouched.
func NewState(a *Archive) *State {
	s := &State{orig: a}
	for i := range a.Layers {
		s.vlayers = append(s.vlayers, VLayer{Parts: []Part{{Source: i}}})
	}
	return s
}

// VLayers returns the current virtual layers.
func (s *State) VLayers() []VLayer { return slices.Clone(s.vlayers) }

// Step is one history entry of the edited image.
type Step struct {
	History
	// Orig is the index of the entry in the original history.
	Orig int
	// Layer is the virtual layer index for non-empty steps, -1 otherwise.
	Layer int
	// Edited marks the step whose layer was changed.
	Edited bool
	// Merged counts original layers folded into this step's layer.
	Merged int
}

// Steps derives the history of the edited image. Layers keep their order;
// a merged layer sits at the position of its last source, and the steps of
// its other sources become metadata-only so their commands stay visible.
// Steps of dropped layers disappear.
func (s *State) Steps() []Step {
	owner := map[int]int{} // original layer -> vlayer
	last := map[int]bool{} // original layer is the last source of its vlayer
	for vi, v := range s.vlayers {
		src := v.sources()
		for _, o := range src {
			owner[o] = vi
		}
		if len(src) > 0 {
			last[src[len(src)-1]] = true
		}
	}

	var out []Step
	layer := 0
	for i, h := range s.orig.History {
		st := Step{History: h, Orig: i, Layer: -1}
		if h.EmptyLayer {
			out = append(out, st)
			continue
		}
		o := layer
		layer++
		vi, ok := owner[o]
		if !ok {
			continue // dropped
		}
		if !last[o] {
			st.EmptyLayer = true
			out = append(out, st)
			continue
		}
		st.Layer = vi
		st.Edited = s.vlayers[vi].Edited
		st.Merged = len(s.vlayers[vi].sources())
		out = append(out, st)
	}
	return out
}

// ErrBadStep is returned when an operation targets a step that does not
// have a layer.
var ErrBadStep = errors.New("step has no filesystem layer")

func (s *State) layerOf(step int) (int, error) {
	steps := s.Steps()
	if step < 0 || step >= len(steps) {
		return 0, fmt.Errorf("step %d out of range", step+1)
	}
	if steps[step].Layer < 0 {
		return 0, fmt.Errorf("step %d: %w", step+1, ErrBadStep)
	}
	return steps[step].Layer, nil
}

// DropLayer removes the layer of a step.
func (s *State) DropLayer(step int) error {
	vi, err := s.layerOf(step)
	if err != nil {
		return err
	}
	if len(s.vlayers) == 1 {
		return errors.New("cannot drop the only layer")
	}
	s.vlayers = slices.Delete(s.vlayers, vi, vi+1)
	return nil
}

// MergeLayers folds the layers of steps from..to (inclusive) into one.
// Metadata-only steps in between are allowed; at least two layers must be
// in the range.
func (s *State) MergeLayers(from, to int) error {
	if from > to {
		from, to = to, from
	}
	steps := s.Steps()
	if from < 0 || to >= len(steps) {
		return errors.New("merge range out of bounds")
	}
	first, lastL := -1, -1
	for _, st := range steps[from : to+1] {
		if st.Layer < 0 {
			continue
		}
		if first < 0 {
			first = st.Layer
		}
		lastL = st.Layer
	}
	if first < 0 || first == lastL {
		return errors.New("select at least two layers to merge")
	}
	merged := VLayer{Edited: true}
	for _, v := range s.vlayers[first : lastL+1] {
		merged.Parts = append(merged.Parts, v.Parts...)
	}
	s.vlayers = slices.Replace(s.vlayers, first, lastL+1, merged)
	return nil
}

// RemovePath undoes a layer's change to p: entries for p and everything
// under it, including deletion markers, leave the layer.
func (s *State) RemovePath(step int, p string) error {
	vi, err := s.layerOf(step)
	if err != nil {
		return err
	}
	p = cleanPath(p)
	if p == "" {
		return errors.New("cannot remove the root directory")
	}
	s.vlayers[vi] = removeFromLayer(s.vlayers[vi], p)
	return nil
}

// PurgePath removes p from every layer, so no version of it stays in the
// image.
func (s *State) PurgePath(p string) error {
	p = cleanPath(p)
	if p == "" {
		return errors.New("cannot purge the root directory")
	}
	for i := range s.vlayers {
		if s.layerHas(i, p) {
			s.vlayers[i] = removeFromLayer(s.vlayers[i], p)
		}
	}
	return nil
}

func (s *State) layerHas(vi int, p string) bool {
	for _, e := range s.layerEntries(vi) {
		if e.Path == p || strings.HasPrefix(e.Path, p+"/") {
			return true
		}
	}
	return false
}

func removeFromLayer(v VLayer, p string) VLayer {
	out := VLayer{Edited: true}
	for _, part := range v.Parts {
		part.Remove = append(slices.Clone(part.Remove), p)
		part.Files = slices.DeleteFunc(slices.Clone(part.Files), func(f NewFile) bool {
			return f.Path == p || strings.HasPrefix(f.Path, p+"/")
		})
		out.Parts = append(out.Parts, part)
	}
	return out
}

// PutFile adds or replaces a file in the layer of a step.
func (s *State) PutFile(step int, f NewFile) error {
	vi, err := s.layerOf(step)
	if err != nil {
		return err
	}
	f.Path = cleanPath(f.Path)
	if f.Path == "" {
		return errors.New("file path is empty")
	}
	if f.Content != nil {
		f.Size = int64(len(f.Content))
	}
	v := s.vlayers[vi]
	v.Parts = slices.Clone(v.Parts)
	v.Edited = true
	if n := len(v.Parts); n > 0 && v.Parts[n-1].Source < 0 {
		last := v.Parts[n-1]
		last.Files = slices.DeleteFunc(slices.Clone(last.Files), func(x NewFile) bool { return x.Path == f.Path })
		last.Files = append(last.Files, f)
		v.Parts[n-1] = last
	} else {
		v.Parts = append(v.Parts, Part{Source: -1, Files: []NewFile{f}})
	}
	s.vlayers[vi] = v
	return nil
}

func cleanPath(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}
