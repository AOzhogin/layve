package image

import "slices"

// LayerDetail is a history step together with the files it changed.
type LayerDetail struct {
	layer   Layer
	changes []FileChange
	edited  bool
	merged  int
}

// NewLayerDetail builds a LayerDetail.
func NewLayerDetail(layer Layer, changes []FileChange) LayerDetail {
	return LayerDetail{layer: layer, changes: slices.Clone(changes)}
}

// Layer returns the history step.
func (d LayerDetail) Layer() Layer { return d.layer }

// Changes returns a copy of the changed files.
func (d LayerDetail) Changes() []FileChange { return slices.Clone(d.changes) }

// WithEdit returns a copy marked as changed by an edit plan; merged counts
// the original layers folded into this one.
func (d LayerDetail) WithEdit(edited bool, merged int) LayerDetail {
	d.edited, d.merged = edited, merged
	return d
}

// Edited reports whether an edit plan changed this layer.
func (d LayerDetail) Edited() bool { return d.edited }

// Merged returns how many original layers this layer combines (0 or 1 when
// not merged).
func (d LayerDetail) Merged() int { return d.merged }
