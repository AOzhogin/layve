package imagearchive

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// ref points at an entry of a part: the index into the source layer's
// Entries, or into the part's Files.
type ref struct {
	part int
	idx  int
}

type item struct {
	ref
	entry Entry
}

// resolved is the content of a virtual layer.
type resolved struct {
	// chosen lists the entries to copy, in part order with hard links last
	// so their targets are written first.
	chosen []item
	// synth holds whiteouts and opaque markers, written after everything
	// else: an opaque marker keeps the entries of its own layer.
	synth []Entry
}

func (r resolved) entries() []Entry {
	out := make([]Entry, 0, len(r.chosen)+len(r.synth))
	for _, it := range r.chosen {
		out = append(out, it.entry)
	}
	return append(out, r.synth...)
}

func (s *State) partItems(pi int, p Part) []item {
	var out []item
	if p.Source < 0 {
		for i, f := range p.Files {
			out = append(out, item{ref: ref{pi, i}, entry: addedEntry(f)})
		}
		return out
	}
	for i, e := range s.orig.Layers[p.Source].Entries {
		if p.removes(e.Path) {
			continue
		}
		out = append(out, item{ref: ref{pi, i}, entry: e})
	}
	return out
}

func addedEntry(f NewFile) Entry {
	e := Entry{Path: f.Path, Size: f.Size, Mode: f.Mode, UID: f.UID, GID: f.GID}
	switch {
	case f.Content == nil:
	case int64(len(f.Content)) > DefaultOptions.MaxFileSize:
		e.Preview = PreviewTooLarge
	case !isText(f.Content):
		e.Preview = PreviewBinary
	default:
		e.Preview, e.Content = PreviewText, f.Content
	}
	return e
}

// resolve applies the parts of a layer in order, like overlay layers
// squashed into one tarball.
func (s *State) resolve(v VLayer) resolved {
	present := map[string]item{}
	hidden := map[string]bool{}
	opaque := map[string]bool{}
	for pi, p := range v.Parts {
		items := s.partItems(pi, p)
		for _, it := range items {
			switch {
			case it.entry.Whiteout:
				dropUnder(present, it.entry.Path, true)
				hidden[it.entry.Path] = true
			case it.entry.Opaque:
				dropUnder(present, it.entry.Path, false)
				opaque[it.entry.Path] = true
			}
		}
		for _, it := range items {
			if !it.entry.Whiteout && !it.entry.Opaque {
				present[it.entry.Path] = it
			}
		}
	}

	var r resolved
	var links []item
	for _, it := range present {
		if it.entry.Hardlink != "" {
			links = append(links, it)
		} else {
			r.chosen = append(r.chosen, it)
		}
	}
	byRef := func(items []item) {
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i].ref, items[j].ref
			if a.part != b.part {
				return a.part < b.part
			}
			return a.idx < b.idx
		})
	}
	byRef(r.chosen)
	byRef(links)
	r.chosen = append(r.chosen, links...)

	// A deleted directory that comes back must hide what lower layers had.
	whiteouts := map[string]bool{}
	for p := range hidden {
		switch {
		case hasPresent(present, p):
			opaque[p] = true
		default:
			whiteouts[p] = true
		}
	}
	for _, p := range sortedKeys(whiteouts) {
		if !hasAncestor(whiteouts, p) {
			r.synth = append(r.synth, Entry{Path: p, Whiteout: true})
		}
	}
	for _, p := range sortedKeys(opaque) {
		if hasPresent(present, p) {
			r.synth = append(r.synth, Entry{Path: p, Dir: true, Opaque: true})
		}
	}
	return r
}

// hasPresent reports whether p or anything under it is present.
func hasPresent(present map[string]item, p string) bool {
	if _, ok := present[p]; ok {
		return true
	}
	prefix := p + "/"
	for q := range present {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

func dropUnder(present map[string]item, p string, self bool) {
	if self {
		delete(present, p)
	}
	prefix := p + "/"
	for q := range present {
		if strings.HasPrefix(q, prefix) {
			delete(present, q)
		}
	}
}

func hasAncestor(set map[string]bool, p string) bool {
	for a := parentOf(p); a != ""; a = parentOf(a) {
		if set[a] {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *State) layerEntries(vi int) []Entry {
	v := s.vlayers[vi]
	if v.untouched() {
		return s.orig.Layers[v.Parts[0].Source].Entries
	}
	return s.resolve(v).entries()
}

// Layers returns the metadata of the edited image's layers, ready for Diff.
func (s *State) Layers() []Layer {
	out := make([]Layer, len(s.vlayers))
	for i := range s.vlayers {
		out[i] = Layer{Entries: s.layerEntries(i)}
	}
	return out
}

// Validate reports problems that would make the edited image fail to load,
// such as hard links whose target is gone.
func (s *State) Validate() error {
	fsys := map[string]bool{}
	for vi := range s.vlayers {
		entries := s.layerEntries(vi)
		for _, e := range entries {
			if e.Whiteout {
				delete(fsys, e.Path)
				for p := range fsys {
					if strings.HasPrefix(p, e.Path+"/") {
						delete(fsys, p)
					}
				}
			}
		}
		here := map[string]bool{}
		for _, e := range entries {
			if !e.Whiteout && !e.Opaque {
				here[e.Path] = true
			}
		}
		for _, e := range entries {
			if e.Hardlink != "" && !here[e.Hardlink] && !fsys[e.Hardlink] {
				return fmt.Errorf("/%s is a hard link to /%s, which no longer exists; remove the link too", e.Path, e.Hardlink)
			}
		}
		for p := range here {
			fsys[p] = true
		}
	}
	return nil
}

// LayerSize sums the file sizes of an edited layer.
func LayerSize(l Layer) int64 {
	var total int64
	for _, e := range l.Entries {
		if e.Mode&fs.ModeType == 0 && !e.Whiteout && !e.Opaque && e.Hardlink == "" {
			total += e.Size
		}
	}
	return total
}
