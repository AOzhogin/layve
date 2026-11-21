package image

// Target names an image for an operation: by reference when it is tagged,
// by ID when it is dangling.
type Target struct {
	ref    Reference
	id     ID
	tagged bool
}

// ReferenceTarget targets a tag.
func ReferenceTarget(ref Reference) Target { return Target{ref: ref, tagged: true} }

// IDTarget targets an image by ID.
func IDTarget(id ID) Target { return Target{id: id} }

// Reference returns the tag and whether the target is tagged.
func (t Target) Reference() (Reference, bool) { return t.ref, t.tagged }

// String returns the reference or the full ID, suitable for the Docker API.
func (t Target) String() string {
	if t.tagged {
		return t.ref.String()
	}
	return t.id.String()
}

// Display returns a short human-readable form.
func (t Target) Display() string {
	if t.tagged {
		return t.ref.String()
	}
	return t.id.Short()
}
